-- SPDX-License-Identifier: AGPL-3.0-only
-- Owned references, never copied target metadata or authorization entitlements.
-- Apply after canonical posts, media, survey, wallet, location and calendar schemas.
CREATE TABLE IF NOT EXISTS owned_post_embeds(
 post_id uuid NOT NULL REFERENCES owned_text_posts(id),position integer NOT NULL CHECK(position BETWEEN 0 AND 15),
 kind text NOT NULL CHECK(kind IN('survey','fund','meet','live_stream','notable_day','calendar_event','location')),
 target_id uuid,location jsonb,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(post_id,kind),UNIQUE(post_id,position),
 CHECK((kind='location' AND target_id IS NULL AND location IS NOT NULL AND jsonb_typeof(location)='object' AND octet_length(location::text)<=8192)OR(kind<>'location' AND target_id IS NOT NULL AND location IS NULL))
);
CREATE TABLE IF NOT EXISTS owned_post_thumbnails(
 post_id uuid PRIMARY KEY REFERENCES owned_text_posts(id),file_id uuid NOT NULL REFERENCES owned_media_files(id)
);
