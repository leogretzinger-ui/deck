-- Content-free native hook facts, independent of lifecycle and PTY activity.
ALTER TABLE sessions ADD COLUMN attention_state TEXT NOT NULL DEFAULT 'unknown'
  CHECK (attention_state IN ('unknown','working','input','approval','idle'));
ALTER TABLE sessions ADD COLUMN attention_sequence INTEGER NOT NULL DEFAULT 0 CHECK (attention_sequence >= 0);
ALTER TABLE sessions ADD COLUMN attention_event_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN attention_turn TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN completion_turn TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN completion_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN completion_seen TEXT NOT NULL DEFAULT '';

-- A new runtime cannot inherit a predecessor's claims. Native /clear or /resume
-- within the same process also invalidates the old conversation's badges, but
-- retains the generation's sequence fence against delayed deliveries.
CREATE TRIGGER trg_attention_generation AFTER UPDATE OF generation ON sessions
WHEN NEW.generation <> OLD.generation
BEGIN
  UPDATE sessions SET attention_state = 'unknown', attention_sequence = 0, attention_event_hash = '',
    attention_turn = '', completion_revision = '', completion_seen = '', completion_turn = '' WHERE id = NEW.id;
END;
CREATE TRIGGER trg_attention_identity AFTER UPDATE OF native_session_id ON sessions
WHEN NEW.native_session_id IS NOT OLD.native_session_id
BEGIN
  UPDATE sessions SET attention_state = 'unknown', attention_turn = '',
    completion_revision = '', completion_seen = '', completion_turn = '' WHERE id = NEW.id;
END;
CREATE TRIGGER trg_attention_stopped AFTER UPDATE OF status, removed_at ON sessions
WHEN NEW.status NOT IN ('starting','running') OR NEW.removed_at IS NOT NULL
BEGIN
  UPDATE sessions SET attention_state = 'unknown', attention_turn = '' WHERE id = NEW.id;
END;
