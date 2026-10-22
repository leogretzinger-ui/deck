-- Migration 002 — a target for the readiness writability probe.
--
-- Readiness must establish that the database is writable RIGHT NOW (NFR-REL9).
-- A Ping cannot: a read-only filesystem, a full disk, or a revoked permission
-- all leave the connection healthy while every write fails.
--
-- The probe needs a real, committed mutation to prove that, and it needs one
-- that is bounded — a probe that inserts a row would grow the database in
-- proportion to uptime. So: exactly one row, updated in place. The CHECK
-- pins it to a single row so the table cannot grow at all.
--
-- This lives in a migration rather than being created on demand so that the
-- schema stays entirely migration-controlled, and so the probe itself performs
-- only a write, never a DDL statement that would silently succeed as a no-op
-- once the table already existed.
CREATE TABLE writability_probe (
  id         INTEGER PRIMARY KEY CONSTRAINT chk_probe_single_row CHECK (id = 1),
  checked_at TEXT NOT NULL
) STRICT;

INSERT INTO writability_probe (id, checked_at) VALUES (1, '1970-01-01T00:00:00.000Z');
