import copy
import hashlib
import importlib.util
import json
import pathlib
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("preview_dns", pathlib.Path(__file__).with_name("preview-dns.py"))
dns = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dns)


class PreviewDNSTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = pathlib.Path(self.temp.name)
        self.cfg = dict(database=str(root / "deck.db"), token_file=str(root / "token"),
                        zone_id="a" * 32, base_domain="example.net", hostname_prefix="main-p-",
                        tunnel_id="11111111-2222-3333-4444-555555555555")
        self.host = "main-p-" + hashlib.sha256(b"synthetic").hexdigest()[:20] + ".example.net"
        with sqlite3.connect(self.cfg["database"]) as db:
            db.execute("CREATE TABLE project_preview_registrations (project_id TEXT, origin TEXT, enabled INTEGER)")
            db.execute("INSERT INTO project_preview_registrations VALUES (?, ?, 0)", ("synthetic", "https://" + self.host))
        self.records = [{"name": "*.example.net", "type": "CNAME", "content": "alt.example.org", "proxied": True}]
        self.writes = []

    def api(self, method, path, data=None):
        if method == "POST":
            self.writes.append(copy.deepcopy(data))
            self.records.append(copy.deepcopy(data))
            return {"success": True, "result": data}
        self.assertEqual(method, "GET")
        if "dns_records?" in path:
            return {"success": True, "result": self.records, "result_info": {"total_pages": 1}}
        return {"success": True, "result": {"name": "example.net"}}

    def test_disabled_reservations_kept_idempotent_wildcard_untouched(self):
        hosts = dns.desired_hosts(self.cfg)
        self.assertEqual(hosts, [self.host])
        before = copy.deepcopy(self.records[0])
        result = dns.reconcile(self.cfg, hosts, self.api)
        self.assertEqual(result["missing"], 1)
        self.assertFalse(self.writes)
        self.assertEqual(dns.reconcile(self.cfg, hosts, self.api, True)["created"], 1)
        self.assertEqual(dns.reconcile(self.cfg, hosts, self.api, True)["created"], 0)
        self.assertEqual(len(self.writes), 1)
        self.assertEqual(self.records[0], before)
        self.assertEqual(self.writes[0]["name"], self.host)
        self.assertTrue(self.writes[0]["proxied"])

    def test_conflicts_fail_before_any_writes(self):
        for field, value in (("type", "A"), ("content", "another.cfargotunnel.com"), ("proxied", False), ("comment", "someone-else")):
            with self.subTest(field=field):
                self.records = []
                self.writes = []
                dns.reconcile(self.cfg, [self.host], self.api, True)
                self.records[0][field] = value
                self.writes = []
                with self.assertRaises(dns.Refused):
                    dns.reconcile(self.cfg, ["new.example.net", self.host], self.api, True)
                self.assertFalse(self.writes)

    def test_unowned_matching_record_is_not_adopted(self):
        dns.reconcile(self.cfg, [self.host], self.api, True)
        del self.records[1]["comment"]
        with self.assertRaises(dns.Refused):
            dns.reconcile(self.cfg, [self.host], self.api, True)

    def test_tampered_or_other_instance_registration_refused(self):
        for origin in ("https://evil.example.org", "https://" + self.host + "/", "https://" + self.host.replace("main-p-", "p-")):
            with sqlite3.connect(self.cfg["database"]) as db:
                db.execute("UPDATE project_preview_registrations SET origin=?", (origin,))
            with self.assertRaises(dns.Refused):
                dns.desired_hosts(self.cfg)

    def test_missing_database_is_not_created(self):
        self.cfg["database"] += "-absent"
        with self.assertRaises(sqlite3.Error):
            dns.desired_hosts(self.cfg)
        self.assertFalse(pathlib.Path(self.cfg["database"]).exists())

    def test_zone_boundary(self):
        self.cfg["base_domain"] = "notexample.net"
        with self.assertRaises(dns.Refused):
            dns.reconcile(self.cfg, [self.host], self.api, True)
        self.assertFalse(self.writes)

    def test_configuration(self):
        path = pathlib.Path(self.temp.name) / "config.json"
        path.write_text(json.dumps(self.cfg))
        self.assertEqual(dns.configuration(path), self.cfg)
        for field, value in (("hostname_prefix", "main.p-"), ("hostname_prefix", "-p"), ("base_domain", "*.example.net"), ("zone_id", "../other"), ("tunnel_id", "arbitrary.example.net"), ("database", "relative")):
            with self.subTest(field=field, value=value):
                cfg = dict(self.cfg, **{field: value})
                path.write_text(json.dumps(cfg))
                with self.assertRaises(dns.Refused):
                    dns.configuration(path)

    def test_token_permissions(self):
        path = pathlib.Path(self.cfg["token_file"])
        path.write_text("synthetic-token")
        path.chmod(0o644)
        with self.assertRaises(dns.Refused):
            dns.Cloudflare(path)
        path.chmod(0o600)
        self.assertEqual(dns.Cloudflare(path).token, "synthetic-token")

    def test_registration_limit(self):
        with sqlite3.connect(self.cfg["database"]) as db:
            db.executemany("INSERT INTO project_preview_registrations VALUES (?, ?, 0)", [(str(i), "invalid") for i in range(1024)])
        with self.assertRaisesRegex(dns.Refused, "limit"):
            dns.desired_hosts(self.cfg)

    def test_long_namespace_fits_free_plan_comment_limit(self):
        self.cfg["hostname_prefix"] = "p" * 43
        self.cfg["base_domain"] = "a" * 63 + "." + "b" * 63 + ".example.net"
        path = pathlib.Path(self.temp.name) / "config.json"
        path.write_text(json.dumps(self.cfg))
        cfg = dns.configuration(path)
        host = cfg["hostname_prefix"] + hashlib.sha256(b"synthetic").hexdigest()[:20] + "." + cfg["base_domain"]
        dns.reconcile(cfg, [host], self.api, True)
        self.assertLessEqual(len(self.writes[0]["comment"]), 100)
        self.assertEqual(dns.reconcile(cfg, [host], self.api, True)["created"], 0)


if __name__ == "__main__":
    unittest.main()
