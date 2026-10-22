-- 018_session_color: an optional operator-assigned color tag for a session.
-- Null means no tag; the tile renders with the default provider glyph
-- color. A non-null value is a design-system color key (e.g. "accent",
-- "signal", "green", "amber").
ALTER TABLE sessions ADD COLUMN color TEXT;
