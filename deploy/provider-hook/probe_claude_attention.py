#!/usr/bin/env python3
"""Real Claude 2.1.308, loopback model, isolated test home, no real account.

Exercises the adapter alongside a blocking native a2a Stop hook. Does not test
Claude's delayed interactive idle_prompt notification or real approvals.
"""
import json
from pathlib import Path
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'mempalace'))
from probe_a2a_claude import Probe, make_model_server, write_claude_home, run


def main():
    version = subprocess.check_output(['claude', '--version'], text=True).strip()
    if not version.startswith('2.1.308 '):
        raise RuntimeError(f'Expected pinned Claude 2.1.308, got {version}')
    snapshots = []

    class Receiver(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            assert self.path == '/api/hooks/attention'
            assert self.headers.get('X-Noisedeck-Hook-Token') == 'a' * 32
            snapshots.append(json.loads(self.rfile.read(int(self.headers['Content-Length']))))
            self.send_response(204)
            self.end_headers()

    receiver = ThreadingHTTPServer(('127.0.0.1', 0), Receiver)
    threading.Thread(target=receiver.serve_forever, daemon=True).start()
    probe = Probe('claude')
    model = make_model_server(probe)
    try:
        adapter = Path(__file__).resolve().with_name('noisedeck-attention-hook')
        recorder = probe.root / 'record.py'
        # Record only native routing/ordering metadata. Forward the original
        # synthetic payload to the real adapter; never consume its stdin first.
        recorder.write_text(
            'import json,sys,subprocess\n'
            'from pathlib import Path\n'
            'event=json.load(sys.stdin)\n'
            f'path=Path({str(probe.root / "events")!r})\n'
            'keys=["hook_event_name","session_id","prompt_id","stop_hook_active"]\n'
            'with path.open("a") as output:\n'
            ' output.write(json.dumps({key:event.get(key) for key in keys})+"\\n")\n'
            f'subprocess.run(["python3",{str(adapter)!r},"claude"], '
            'input=json.dumps(event),text=True,check=True)\n'
        )
        write_claude_home(probe, [
            (name, f'python3 {recorder}', 25)
            for name in ['UserPromptSubmit', 'Stop', 'SessionEnd']
        ])
        attention = probe.root / 'attention'
        attention.mkdir(mode=0o700)
        env = probe.env({
            'NOISEDECK_SESSION_HOOK_URL': f'http://127.0.0.1:{receiver.server_port}/api/hooks/capture',
            'NOISEDECK_SESSION_HOOK_TOKEN': 'a' * 32,
            'NOISEDECK_ATTENTION_DIR': str(attention),
            'ANTHROPIC_BASE_URL': f'http://127.0.0.1:{model.server_port}',
            'ANTHROPIC_API_KEY': 'dummy',
            'ANTHROPIC_AUTH_TOKEN': 'dummy',
            'ANTHROPIC_MODEL': 'claude-sonnet-4-5',
            'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC': '1',
            'DISABLE_TELEMETRY': '1',
            'DISABLE_AUTOUPDATER': '1',
            'DISABLE_ERROR_REPORTING': '1',
            'CLAUDE_CONFIG_DIR': str(probe.root / 'home' / '.claude'),
        })
        result = run(probe, ['claude', '-p', 'reply ok', '--output-format', 'text'], env, timeout=90)
        events = [json.loads(line) for line in (probe.root / 'events').read_text().splitlines()]
        stops = [event for event in events if event['hook_event_name'] == 'Stop']
        assert result == 0 and len(stops) >= 2, (result, events)
        assert stops[0]['prompt_id'] and stops[0]['prompt_id'] == stops[1]['prompt_id']
        assert stops[0]['stop_hook_active'] is False and stops[1]['stop_hook_active'] is True
        assert [event['kind'] for event in snapshots] == ['started', 'unknown'], snapshots
        print('PASS real Claude 2.1.308: blocked Stop continued same prompt; '
              'adapter emitted start/lifecycle only, no false completion')
    finally:
        receiver.shutdown()
        receiver.server_close()
        model.shutdown()
        model.server_close()
        probe.cleanup()


if __name__ == '__main__':
    main()
