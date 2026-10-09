-- SPDX-License-Identifier: AGPL-3.0-only
-- Explicitly apply after canonical authority, social, media and calendar schemas.
-- References only: no copied ownership, byte storage, public flag or quota state.
CREATE TABLE IF NOT EXISTS owned_calendar_event_media (
 event_id uuid NOT NULL REFERENCES owned_calendar_events(id),
 slot text NOT NULL CHECK(slot IN('icon','background')),
 file_id uuid NOT NULL REFERENCES owned_media_files(id),
 attached_at timestamptz NOT NULL,
 PRIMARY KEY(event_id,slot)
);
CREATE INDEX IF NOT EXISTS owned_calendar_event_media_file ON owned_calendar_event_media(file_id,event_id);
