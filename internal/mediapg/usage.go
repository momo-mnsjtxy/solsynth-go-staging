// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import _ "embed"

// UsageSchema explicitly upgrades the existing owned file metadata contract.
// It is never applied implicitly by constructor, readiness, or request handlers.
//
//go:embed usage_schema.sql
var UsageSchema string
