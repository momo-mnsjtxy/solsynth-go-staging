-- SPDX-License-Identifier: AGPL-3.0-only
-- Preserve existing audiences while adding a current-state friend projection.
ALTER TABLE notification_v1.events DROP CONSTRAINT IF EXISTS events_audience_check;
ALTER TABLE notification_v1.events ADD CONSTRAINT events_audience_check CHECK(audience IN('account','notification','chat','friend'));
