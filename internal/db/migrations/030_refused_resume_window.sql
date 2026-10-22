-- A session records when its current generation started and whether that
-- generation was launched by resuming a recorded identity.
--
-- Together these answer one question no other column can: did the provider
-- accept the identity we handed it? A provider that does not recognise an id
-- prints its complaint and exits within seconds, so a resumed generation with
-- a very short life is a refusal, and one that keeps running is an acceptance.
--
-- The evidence has to be the process rather than the output. Measured in
-- production: omp announces its session id at process launch but only writes
-- the session to disk once the conversation has content, so a session that is
-- launched and never typed into leaves a recorded id that omp will never
-- recognise. Deck replayed one such id every ten minutes to generation 11,
-- each attempt answered with `Error: Session "..." not found.` — the identity
-- was never going to work, and nothing in the schema could tell.
--
-- generation_started_at is NULL for rows that predate this migration, which
-- withholds the judgement rather than guessing it: a NULL start time proves
-- no lifetime and must never be read as a short one.
ALTER TABLE sessions ADD COLUMN generation_started_at TEXT;

ALTER TABLE sessions ADD COLUMN generation_resumed INTEGER NOT NULL DEFAULT 0
  CONSTRAINT chk_sessions_generation_resumed CHECK (generation_resumed IN (0, 1));
