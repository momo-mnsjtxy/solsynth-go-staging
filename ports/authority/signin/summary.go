// SPDX-License-Identifier: AGPL-3.0-only
package signin

import "time"

// FactorSummary deliberately cannot carry secrets, recovery material, counters,
// provisioning URIs or configuration. Unknown canonical kinds stay unspecified.
type FactorSummary struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Trustworthy int            `json:"trustworthy"`
	HasSecret   bool           `json:"has_secret"`
	Config      map[string]any `json:"config"`
	EnabledAt   *time.Time     `json:"enabled_at"`
	ExpiredAt   *time.Time     `json:"expired_at"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   *time.Time     `json:"updated_at"`
}
