-- Handoffs are permanent until explicitly deleted. Keep the legacy NOT NULL
-- expires_at column for rolling-deployment compatibility, but neutralize it and
-- remove expiry metadata from existing payloads.
DROP INDEX IF EXISTS handoffs_expires_at_idx;

UPDATE handoffs
SET expires_at = 0,
    payload = json_remove(payload, '$.handoff.expires_at');
