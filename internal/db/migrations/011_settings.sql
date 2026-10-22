-- 011_settings: console-wide operator settings (operator direction
-- 2026-08-06). One key/value row per setting, written only by the
-- settings endpoint and read at session launch.
--
-- A table rather than the config file because these are OPERATOR
-- choices made from the UI at runtime: the config file is deployment
-- state, edited by whoever runs the container, and a UI that rewrote it
-- would give one fact two owners. The values here are console
-- behavior, never credentials — NFR-SEC6 keeps secrets out of the
-- database exactly as it keeps them out of argv.
--
-- Plain table creation, so no rebuild and no foreign_keys toggle.

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
