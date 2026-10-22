#!/usr/bin/env python3
"""Forward lifecycle checkpoints to Deck's one MemPalace writer."""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import fcntl
import hashlib
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path


SAVE_INTERVAL = 15
RECENT_MESSAGE_COUNT = 30
MCP_URL = "http://127.0.0.1:8765/mcp"
ROOM = "session_checkpoints"
TRANSIENT_RPC_ERRORS = {-32603}
TRANSIENT_HTTP_ERRORS = {408, 409, 425, 429}
ENCODED_PARENT_PREFIXES = (
    "git-",
    "dev-",
    "projects-",
    "Projects-",
    "src-",
    "code-",
    "work-",
    "Documents-",
)


def _palace_root() -> Path:
    return Path.home() / ".mempalace"


def _state_dir() -> Path:
    return _palace_root() / "hook_state"


def _outbox_dir() -> Path:
    override = os.environ.get("NOISEDECK_MEMPALACE_HOOK_OUTBOX", "")
    return Path(override).expanduser() if override else _palace_root() / "hook_outbox"


def _ensure_private_dir(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.chmod(0o700)


def _fsync_dir(path: Path) -> None:
    try:
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    except OSError:
        return
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def _log(event: str, job_id: str = "") -> None:
    if not _palace_root().is_dir():
        return
    try:
        state = _state_dir()
        _ensure_private_dir(state)
        path = state / "forwarder.log"
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        with os.fdopen(fd, "a", encoding="utf-8") as handle:
            stamp = dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds")
            suffix = f" job={job_id}" if job_id else ""
            handle.write(f"{stamp} event={event}{suffix}\n")
    except OSError:
        pass


def _sanitize_session_id(value: object) -> str:
    sanitized = re.sub(r"[^a-zA-Z0-9_-]", "", str(value or ""))
    return sanitized or "unknown"


def _transcript_roots(harness: str) -> tuple[Path, ...]:
    override = os.environ.get("NOISEDECK_MEMPALACE_TRANSCRIPT_ROOTS", "")
    if override:
        return tuple(Path(item).expanduser().resolve() for item in override.split(os.pathsep))
    relative = {
        "claude-code": ".claude/projects",
        "codex": ".codex/sessions",
        "pi": ".pi/agent/sessions",
        "omp": ".omp/agent/sessions",
    }[harness]
    return ((Path.home() / relative).resolve(),)


def _validate_transcript_path(value: object, harness: str) -> Path | None:
    raw = str(value or "")
    if not raw or ".." in Path(raw).parts:
        return None
    try:
        path = Path(raw).expanduser().resolve()
    except OSError:
        return None
    if path.suffix not in (".json", ".jsonl") or not path.is_file():
        return None
    if not any(path.is_relative_to(root) for root in _transcript_roots(harness)):
        return None
    return path


def _message_text(entry: dict) -> str | None:
    message = entry.get("message") or entry.get("event_message") or {}
    if isinstance(message, dict) and message.get("role") == "user":
        content = message.get("content", "")
        if isinstance(content, list):
            content = " ".join(
                block.get("text", "")
                for block in content
                if isinstance(block, dict) and isinstance(block.get("text", ""), str)
            )
        if isinstance(content, str):
            return content

    if entry.get("type") == "event_msg":
        payload = entry.get("payload") or {}
        if isinstance(payload, dict) and payload.get("type") == "user_message":
            content = payload.get("message", "")
            if isinstance(content, str):
                return content
    return None


def _scan_transcript(path: Path) -> tuple[int, list[str], str | None]:
    count = 0
    recent: collections.deque[str] = collections.deque(maxlen=RECENT_MESSAGE_COUNT)
    cwd = None
    try:
        with path.open("r", encoding="utf-8", errors="replace") as handle:
            for line_number, line in enumerate(handle):
                try:
                    entry = json.loads(line)
                except (json.JSONDecodeError, TypeError):
                    continue
                if not isinstance(entry, dict):
                    continue
                if cwd is None and line_number < 200:
                    candidate = entry.get("cwd")
                    if isinstance(candidate, str) and candidate.strip():
                        cwd = candidate
                text = _message_text(entry)
                if text is None:
                    continue
                if "<command-message>" in text or "<system-reminder>" in text:
                    continue
                text = text.strip()
                if not text:
                    continue
                count += 1
                recent.append(text[:200])
    except OSError:
        return 0, [], cwd
    return count, list(recent), cwd


def _safe_wing_slug(value: str) -> str:
    slug = value.lower().replace(" ", "_").replace("-", "_")
    slug = re.sub(r"[^\w.']+", "_", slug)
    slug = re.sub(r"\.{2,}", ".", slug)
    slug = slug[:120].strip("_.'")
    return slug or "sessions"


def _wing_for_transcript(path: Path, cwd: str | None) -> str:
    if cwd:
        project = cwd.replace("\\", "/").rstrip("/").rsplit("/", 1)[-1]
        if project:
            return f"wing_{_safe_wing_slug(project)}"

    normalized = str(path).replace("\\", "/")
    match = re.search(r"/\.claude/projects/-([^/]+)", normalized)
    if match:
        encoded = match.group(1)
        home_match = re.match(r"(?:Users|home)-[^-]+-(.+)", encoded)
        if home_match:
            encoded = home_match.group(1)
        for prefix in ENCODED_PARENT_PREFIXES:
            if encoded.startswith(prefix):
                encoded = encoded[len(prefix) :]
                break
        return f"wing_{_safe_wing_slug(encoded)}"
    return "wing_sessions"


def _auto_save_enabled() -> bool:
    config_path = _palace_root() / "config.json"
    try:
        config = json.loads(config_path.read_text(encoding="utf-8"))
        value = config.get("hooks", {}).get("auto_save", True)
        return value if isinstance(value, bool) else True
    except (OSError, json.JSONDecodeError, AttributeError):
        return True


def _checkpoint_request(
    *,
    session_id: str,
    harness: str,
    path: Path,
    message_count: int,
    recent: list[str],
    cwd: str | None,
) -> dict:
    agent = "claude" if harness == "claude-code" else harness
    content = (
        f"CHECKPOINT:{dt.datetime.now().strftime('%Y-%m-%d')}"
        f"|session:{session_id}|msgs:{message_count}"
        f"|recent:{'|'.join(message[:80] for message in recent[-10:])}"
    )
    arguments = {
        "wing": _wing_for_transcript(path, cwd),
        "room": ROOM,
        "content": content,
        "added_by": agent,
    }
    # MemPalace derives the drawer ID from wing, room, and content and
    # upserts that ID. Replaying these exact arguments is therefore idempotent;
    # the JSON-RPC ID below is correlation metadata, not the dedup mechanism.
    canonical = json.dumps(arguments, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    job_id = hashlib.sha256(canonical.encode("utf-8")).hexdigest()
    return {
        "jsonrpc": "2.0",
        "id": f"hook-{job_id[:24]}",
        "method": "tools/call",
        "params": {"name": "mempalace_add_drawer", "arguments": arguments},
    }


def _spool(request: dict) -> str:
    outbox = _outbox_dir()
    _ensure_private_dir(outbox)
    job_id = str(request["id"]).removeprefix("hook-")
    final_path = outbox / f"{job_id}.ready"
    if final_path.exists():
        return job_id

    temporary = outbox / f".{job_id}.{os.getpid()}.{uuid.uuid4().hex}.tmp"
    payload = {
        "version": 1,
        "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "request": request,
    }
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, ensure_ascii=False, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, final_path)
        final_path.chmod(0o600)
        _fsync_dir(outbox)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass
    return job_id


def _write_marker(path: Path, value: int) -> None:
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        handle.write(str(value))
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, path)
    _fsync_dir(path.parent)


def _enqueue_hook(hook: str, harness: str, data: dict) -> None:
    if not _palace_root().is_dir() or not _auto_save_enabled():
        return
    if hook == "session-start":
        _ensure_private_dir(_state_dir())
        return

    path = _validate_transcript_path(data.get("transcript_path"), harness)
    if path is None:
        if data.get("transcript_path"):
            _log("invalid_transcript")
        return

    session_id = _sanitize_session_id(data.get("session_id"))
    state = _state_dir()
    _ensure_private_dir(state)
    lock_path = state / f"{session_id}.lock"
    lock_fd = os.open(lock_path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        with os.fdopen(lock_fd, "r+") as lock_handle:
            fcntl.flock(lock_handle, fcntl.LOCK_EX)
            count, recent, cwd = _scan_transcript(path)
            if count == 0 or not recent:
                return

            marker = state / f"{session_id}_last_save"
            if hook == "stop":
                try:
                    last_save = int(marker.read_text(encoding="utf-8").strip())
                except (OSError, ValueError):
                    last_save = 0
                if count < last_save:
                    last_save = 0
                    _log("marker_reset")
                if count - last_save < SAVE_INTERVAL:
                    return

            request = _checkpoint_request(
                session_id=session_id,
                harness=harness,
                path=path,
                message_count=count,
                recent=recent,
                cwd=cwd,
            )
            job_id = _spool(request)
            if hook == "stop":
                _write_marker(marker, count)
            elif hook == "session-end":
                try:
                    marker.unlink()
                except FileNotFoundError:
                    pass
            _log("queued", job_id)
    except OSError:
        _log("queue_failed")


def _decode_result(response: dict) -> tuple[str, str]:
    error = response.get("error")
    if isinstance(error, dict):
        code = error.get("code")
        if code == -32001:
            return "transient", "writer_busy"
        if code in TRANSIENT_RPC_ERRORS:
            return "transient", f"rpc_{code}"
        return "permanent", f"rpc_{code}"

    try:
        content = response["result"]["content"]
        text = next(item["text"] for item in content if item.get("type") == "text")
        result = json.loads(text)
    except (KeyError, TypeError, StopIteration, json.JSONDecodeError):
        return "permanent", "invalid_response"

    if not isinstance(result, dict):
        return "permanent", "invalid_result"
    if result.get("success") is True:
        return "success", "delivered"
    if result.get("error") or result.get("success") is False:
        return "transient", "checkpoint_error"
    return "permanent", "invalid_result"


def _deliver(request: dict) -> tuple[str, str]:
    url = os.environ.get("NOISEDECK_MEMPALACE_MCP_URL", MCP_URL)
    try:
        parsed = urllib.parse.urlsplit(url)
    except ValueError:
        return "permanent", "invalid_mcp_url"
    if (
        parsed.scheme != "http"
        or parsed.hostname not in ("127.0.0.1", "localhost", "::1")
        or parsed.path != "/mcp"
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        return "permanent", "invalid_mcp_url"
    try:
        timeout = float(os.environ.get("NOISEDECK_MEMPALACE_HOOK_TIMEOUT_SECONDS", "5"))
    except ValueError:
        timeout = 5.0
    timeout = min(30.0, max(0.1, timeout))
    body = json.dumps(request, ensure_ascii=False).encode("utf-8")
    http_request = urllib.request.Request(
        url,
        data=body,
        headers={"Accept": "application/json", "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(http_request, timeout=timeout) as response:
            return _decode_result(json.loads(response.read().decode("utf-8")))
    except urllib.error.HTTPError as error:
        if error.code in TRANSIENT_HTTP_ERRORS or error.code >= 500:
            return "transient", f"http_{error.code}"
        return "permanent", f"http_{error.code}"
    except (UnicodeDecodeError, json.JSONDecodeError):
        return "permanent", "invalid_response"
    except (urllib.error.URLError, TimeoutError, OSError):
        return "transient", "transport_error"


def _dead_letter(path: Path, reason: str, job_id: str) -> None:
    failed = path.with_suffix(".failed")
    try:
        os.replace(path, failed)
        _fsync_dir(path.parent)
    except OSError:
        pass
    _log(reason, job_id)


def _ready_jobs(outbox: Path, limit: int) -> list[Path]:
    candidates: list[tuple[int, str, Path]] = []
    for path in outbox.glob("*.ready"):
        try:
            candidates.append((path.stat().st_mtime_ns, path.name, path))
        except OSError:
            continue
    candidates.sort()
    return [path for _mtime, _name, path in candidates[:limit]]


def _requeue(path: Path, reason: str, job_id: str) -> None:
    if reason not in ("transport_error", "writer_busy"):
        try:
            envelope = json.loads(path.read_text(encoding="utf-8"))
            attempts = int(envelope.get("attempts", 0)) + 1
            try:
                maximum = int(
                    os.environ.get("NOISEDECK_MEMPALACE_HOOK_MAX_ATTEMPTS", "16")
                )
            except ValueError:
                maximum = 16
            maximum = min(1000, max(1, maximum))
            if attempts >= maximum:
                _dead_letter(path, "retry_exhausted", job_id)
                return
            envelope["attempts"] = attempts
            envelope["last_failure"] = reason
            temporary = path.with_name(
                f".{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp"
            )
            fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as handle:
                    json.dump(envelope, handle, ensure_ascii=False, sort_keys=True)
                    handle.write("\n")
                    handle.flush()
                    os.fsync(handle.fileno())
                os.replace(temporary, path)
            finally:
                temporary.unlink(missing_ok=True)
        except (OSError, ValueError, TypeError, json.JSONDecodeError):
            _log("requeue_failed", job_id)
            return
    try:
        os.utime(path, None)
        _fsync_dir(path.parent)
    except OSError:
        _log("requeue_failed", job_id)
        return
    _log(reason, job_id)


def _drain() -> None:
    outbox = _outbox_dir()
    if not outbox.is_dir():
        return
    lock_fd = os.open(outbox / ".drain.lock", os.O_RDWR | os.O_CREAT, 0o600)
    with os.fdopen(lock_fd, "r+") as lock_handle:
        try:
            fcntl.flock(lock_handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return

        try:
            limit = int(os.environ.get("NOISEDECK_MEMPALACE_HOOK_DRAIN_LIMIT", "16"))
        except ValueError:
            limit = 16
        limit = min(256, max(1, limit))
        for path in _ready_jobs(outbox, limit):
            job_id = path.stem
            try:
                envelope = json.loads(path.read_text(encoding="utf-8"))
                request = envelope["request"]
                if envelope.get("version") != 1 or not isinstance(request, dict):
                    raise ValueError("unsupported envelope")
            except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError):
                _dead_letter(path, "malformed_job", job_id)
                continue

            status = "transient"
            reason = "transport_error"
            for attempt in range(2):
                status, reason = _deliver(request)
                if status != "transient" or attempt == 1:
                    break
                time.sleep(0.25)

            if status == "success":
                try:
                    path.unlink()
                    _fsync_dir(outbox)
                except OSError:
                    _log("ack_failed", job_id)
                    continue
                _log(reason, job_id)
                continue
            if status == "permanent":
                _dead_letter(path, reason, job_id)
                continue
            _requeue(path, reason, job_id)


def _start_drainer() -> None:
    if os.environ.get("NOISEDECK_MEMPALACE_HOOK_AUTODRAIN", "1") == "0":
        return
    try:
        subprocess.Popen(
            [sys.executable, str(Path(__file__).resolve()), "--drain"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            close_fds=True,
            start_new_session=True,
        )
    except OSError:
        _log("drainer_spawn_failed")


def _hook_main(hook: str, harness: str) -> None:
    try:
        data = json.load(sys.stdin)
        if not isinstance(data, dict):
            data = {}
    except (json.JSONDecodeError, OSError):
        data = {}
        _log("invalid_hook_payload")
    try:
        try:
            _enqueue_hook(hook, harness, data)
        except Exception:
            _log("queue_failed")
    finally:
        _start_drainer()
        print("{}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--hook", choices=("session-start", "stop", "session-end", "precompact"))
    parser.add_argument("--harness", choices=("claude-code", "codex", "pi", "omp"))
    parser.add_argument("--drain", action="store_true")
    args = parser.parse_args()
    if args.drain:
        _drain()
        return
    if not args.hook or not args.harness:
        parser.error("--hook and --harness are required unless --drain is used")
    _hook_main(args.hook, args.harness)


if __name__ == "__main__":
    main()
