-- SPDX-License-Identifier: AGPL-3.0-only
SELECT EXISTS(SELECT 1 FROM owned_notable_days WHERE id=$2 AND deleted_at IS NULL AND $1::text IS NOT NULL)
