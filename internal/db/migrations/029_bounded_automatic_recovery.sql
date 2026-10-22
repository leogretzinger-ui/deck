-- A session records how many times automatic recovery has relaunched it, and
-- when that series began.
--
-- resume_attempts cannot carry this. It is a per-generation flag: a committed
-- recovery clears it, because a generation that reached verified-live and was
-- then lost has genuinely earned another attempt. That is correct for one
-- lost generation and unbounded across many — a provider that starts, commits,
-- and exits again relaunches forever, once per reconciliation interval, with
-- nobody watching. Measured in production: one pi session reached generation
-- 2881 and one cline session 3594, relaunching every fifteen seconds.
--
-- These two columns bound the series instead of the attempt. The count is
-- nullable-free (a row has always made zero recoveries) and the series
-- timestamp is NULL until the first one, so existing rows need no backfill.
ALTER TABLE sessions ADD COLUMN automatic_recoveries INTEGER NOT NULL DEFAULT 0
  CONSTRAINT chk_sessions_automatic_recoveries CHECK (automatic_recoveries >= 0);

ALTER TABLE sessions ADD COLUMN automatic_recovery_series_at TEXT;
