-- Migration 004 — the dirty input line (FR-Q15).
--
-- After proxied raw input contains bytes since the last observed Enter,
-- Ctrl-C, or explicit operator clear, the session's input line is DIRTY:
-- composer release pauses with a visible reason, because injecting a paste
-- into a line already holding half-typed text would corrupt both.
--
-- The flag lives on the sessions row, not in process memory, for two
-- reasons. Fail-closed restarts: typed-but-unsubmitted text survives a
-- backend restart inside the live tmux pane, so the dirty flag must
-- survive with it — an in-memory flag would silently launder the line
-- clean. And the claim fence: the composer's claim transaction re-reads
-- the sessions row it is about to inject into, so the dirty check joins
-- the same single serialized read as generation, pane, and status (the
-- PR #22 lesson: never compare two copies of one stale snapshot).
ALTER TABLE sessions
  ADD COLUMN input_dirty INTEGER NOT NULL DEFAULT 0
  CONSTRAINT chk_sessions_input_dirty CHECK (input_dirty IN (0, 1));

ALTER TABLE sessions ADD COLUMN input_dirty_since TEXT;

-- An OPEN bracketed paste spans attachments: the browser may send
-- CSI 200 ~ and disconnect before CSI 201 ~, leaving the pane holding
-- pasted text with the terminal still in paste mode. A fresh attachment
-- whose parser started at ground would read the next CR as a submit and
-- clear the dirty flag over text that is still pending, so the paste state
-- is persisted with the same fail-closed discipline as the flag itself.
ALTER TABLE sessions
  ADD COLUMN input_paste_open INTEGER NOT NULL DEFAULT 0
  CONSTRAINT chk_sessions_input_paste_open CHECK (input_paste_open IN (0, 1));
