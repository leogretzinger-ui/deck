-- A resume must consult the store the original conversation wrote, and a
-- store that moved or vanished must be a NAMED condition, never a silent
-- wrong-directory lookup.
--
-- Claude's SessionStart hook reports transcript_path — the exact ledger file
-- of the conversation (<store-root>/projects/<cwd-slug>/<session-uuid>.jsonl).
-- Every accepted capture records it here, together with the derived store
-- root (the CLAUDE_CONFIG_DIR-equivalent ancestor), so recovery can compare
-- the recorded store against what the next launch would resolve and pin or
-- refuse by name instead of resuming into the wrong directory.
--
-- Both are NULL for rows that predate this migration or whose provider's
-- hook does not report a transcript; a NULL store makes no claim and gates
-- nothing. The columns follow the conversation: an identity roll rewrites
-- them with the new ledger, and forgetting an identity clears them with it.
ALTER TABLE sessions ADD COLUMN native_transcript_path TEXT;

ALTER TABLE sessions ADD COLUMN native_store_root TEXT;
