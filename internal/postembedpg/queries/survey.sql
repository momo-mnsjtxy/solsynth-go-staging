-- SPDX-License-Identifier: AGPL-3.0-only
SELECT EXISTS(SELECT 1 FROM owned_surveys s WHERE s.id=$2 AND s.publisher_id=$3 AND s.deleted_at IS NULL AND s.status=1 AND s.published_at<=clock_timestamp() AND(s.ended_at IS NULL OR s.ended_at>clock_timestamp())AND(s.post_id IS NULL OR s.post_id=$4) AND $1::text IS NOT NULL)
