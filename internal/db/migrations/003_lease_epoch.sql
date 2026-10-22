-- Migration 003 — the lease incarnation.
--
-- (controller_id, fencing_token) does not uniquely identify a lease
-- incarnation: an expired lease reclaimed by the SAME controller keeps both
-- (the token stands because no live holder was displaced — see
-- internal/lease/manager.go). But FR-Q7 counts expiry as controller loss: a
-- composer release authorized before the gap must not be claimable after
-- it, even though every identifier it recorded still matches.
--
-- The incarnation is a random 63-bit id drawn fresh on every ACQUISITION
-- (first acquire, expired reclaim, deliberate takeover) and preserved
-- across renewals. Random rather than a timestamp or counter: a timestamp
-- collides within one stored clock tick, and a counter restarts when
-- release deletes the row — distinctness is the whole requirement, and
-- randomness is the only source here that survives both.
ALTER TABLE controller_leases
  ADD COLUMN incarnation INTEGER NOT NULL DEFAULT 0;
