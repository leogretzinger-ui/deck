-- 007: operator-assigned session display names (task 48). Empty means the
-- row renders its provider, exactly as before the column existed.
ALTER TABLE sessions ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
