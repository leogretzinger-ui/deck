-- Migration 005 — retire the composer/outbox backend (PRD §7.4, §5).
--
-- The ratified MVP removed every durable input surface: no composer, no
-- message queue, no input outbox, and no held or queued input (FR-Q1). The
-- database is no longer the authority for input intents (PRD §9.1: "The
-- database never stores terminal input or output"), and FR-Q4 explicitly
-- disclaims a durable write ledger, so the write ledger 004 added to the
-- sessions table goes with the outbox.
--
-- Order matters and is load-bearing under foreign_keys=ON:
--
--   1. drop idx_audit_message, which indexes the column being removed;
--   2. drop audit_events.message_id — removing the column removes its FK
--      declaration, so no reference to composer_messages.id remains;
--   3. drop composer_messages itself, now unreferenced;
--   4. drop the durable input-line ledger 004 added to sessions. These
--      columns exist only if 004 ran; every deployed database is at 004 or
--      later, so they are dropped unconditionally.
--
-- Each migration runs in its own transaction (migrations.go applyOne), so
-- this block is atomic: a failure leaves the database at version 004.
--
-- The drop is content-free by design (NFR-SEC7): the composer_messages
-- bodies are removed without ever being read, logged, or replayed. FR-Q5
-- and FR-Q8 forbid replay after recovery, and this is the point of no
-- return for the outbox vocabulary the glossary deletes (§5).
DROP INDEX IF EXISTS idx_audit_message;

ALTER TABLE audit_events DROP COLUMN message_id;

DROP TABLE composer_messages;

ALTER TABLE sessions DROP COLUMN input_dirty;
ALTER TABLE sessions DROP COLUMN input_dirty_since;
ALTER TABLE sessions DROP COLUMN input_paste_open;
