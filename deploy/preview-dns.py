#!/usr/bin/env python3
"""Add exact Cloudflare records for one installation's retained preview origins.

Run on the Deck host, never inside an agent workspace. This reads routing
metadata only, does not change Deck's database, and never edits/deletes DNS.
"""

import argparse
import hashlib
import json
import pathlib
import re
import sqlite3
import sys
import urllib.error
import urllib.parse
import urllib.request


class Refused(Exception):
    pass


def configuration(path):
    cfg = json.loads(pathlib.Path(path).read_text())
    required = {"database", "token_file", "zone_id", "base_domain", "hostname_prefix", "tunnel_id"}
    if set(cfg) != required or not all(isinstance(v, str) and v for v in cfg.values()):
        raise Refused("invalid configuration fields")
    if not re.fullmatch(r"[0-9a-f]{32}", cfg["zone_id"]):
        raise Refused("invalid zone ID")
    if not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", cfg["tunnel_id"]):
        raise Refused("invalid tunnel ID")
    domain = cfg["base_domain"]
    prefix = cfg["hostname_prefix"]
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,42}", prefix):
        raise Refused("invalid hostname prefix")
    if len(domain) + len(prefix) + 21 > 253 or "." not in domain or any(
        not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label)
        for label in domain.split(".")
    ):
        raise Refused("invalid preview domain")
    for field in ("database", "token_file"):
        if not pathlib.Path(cfg[field]).is_absolute():
            raise Refused("paths must be absolute")
    return cfg


def desired_hosts(cfg):
    uri = pathlib.Path(cfg["database"]).as_uri() + "?mode=ro"
    with sqlite3.connect(uri, uri=True, timeout=5) as db:
        db.execute("PRAGMA query_only=ON")
        rows = db.execute("SELECT project_id, origin FROM project_preview_registrations LIMIT 1025").fetchall()
    if len(rows) > 1024:
        raise Refused("registration limit exceeded")
    hosts = []
    for project_id, origin in rows:
        if not isinstance(project_id, str) or not 1 <= len(project_id) <= 256:
            raise Refused("invalid registration ID")
        suffix = hashlib.sha256(project_id.encode()).hexdigest()[:20]
        host = cfg["hostname_prefix"] + suffix + "." + cfg["base_domain"]
        if origin != "https://" + host:
            raise Refused("registration does not match the configured namespace")
        hosts.append(host)
    return sorted(set(hosts))


class Cloudflare:
    def __init__(self, token_path):
        path = pathlib.Path(token_path)
        if path.stat().st_mode & 0o077:
            raise Refused("DNS token file must be owner-only")
        self.token = path.read_text().strip()
        if not self.token or any(c.isspace() for c in self.token):
            raise Refused("invalid DNS token file")

    def __call__(self, method, path, data=None):
        request = urllib.request.Request(
            "https://api.cloudflare.com/client/v4" + path,
            data=None if data is None else json.dumps(data).encode(),
            headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"},
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                raw = response.read(4 * 1024 * 1024 + 1)
            if len(raw) > 4 * 1024 * 1024:
                raise Refused("Cloudflare response exceeded limit")
            result = json.loads(raw)
        except (urllib.error.URLError, ValueError, TimeoutError):
            # Do not emit API bodies, headers or credential-bearing exceptions.
            raise Refused("Cloudflare request failed; next run will read state before retrying") from None
        if result.get("success") is not True:
            raise Refused("Cloudflare refused the request")
        return result


def reconcile(cfg, hosts, api, apply=False):
    zone_path = "/zones/" + cfg["zone_id"]
    zone_name = api("GET", zone_path)["result"]["name"]
    if cfg["base_domain"] != zone_name and not cfg["base_domain"].endswith("." + zone_name):
        raise Refused("configured preview domain is outside the selected zone")
    records = []
    for page in range(1, 6):
        response = api("GET", zone_path + "/dns_records?" + urllib.parse.urlencode({"per_page": 1000, "page": page}))
        records.extend(response["result"])
        pages = response["result_info"]["total_pages"]
        if pages > 5:
            raise Refused("zone exceeds bounded record inventory")
        if page >= pages:
            break
    target = cfg["tunnel_id"] + ".cfargotunnel.com"
    namespace = cfg["hostname_prefix"] + "\0" + cfg["base_domain"]
    owner = "deck-preview:v1:" + hashlib.sha256(namespace.encode()).hexdigest()
    missing = []
    for host in hosts:
        matches = [r for r in records if r["name"] == host]
        if not matches:
            missing.append(host)
        elif len(matches) != 1 or any(
            r.get("type") != "CNAME" or r.get("content") != target
            or r.get("proxied") is not True or r.get("comment") != owner
            for r in matches
        ):
            raise Refused("existing DNS record conflicts with preview ownership")
    # Validate every conflict before the first write. No PATCH/DELETE is allowed.
    if apply:
        for host in missing:
            api("POST", zone_path + "/dns_records", {
                "type": "CNAME", "name": host, "content": target,
                "proxied": True, "ttl": 1, "comment": owner,
            })
    return {"registrations": len(hosts), "missing": len(missing), "created": len(missing) if apply else 0}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config")
    parser.add_argument("--apply", action="store_true", help="create missing records; default is read-only")
    args = parser.parse_args()
    try:
        cfg = configuration(args.config)
        result = reconcile(cfg, desired_hosts(cfg), Cloudflare(cfg["token_file"]), args.apply)
    except (Refused, OSError, ValueError, KeyError, TypeError, sqlite3.Error) as error:
        message = str(error) if isinstance(error, Refused) else "configuration or metadata unavailable"
        print("preview-dns: " + message, file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
