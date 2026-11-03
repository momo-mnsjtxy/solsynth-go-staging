// SPDX-License-Identifier: AGPL-3.0-only
package signin

import "encoding/json"

// Current SDKs require bookkeeping and nullable fields on every factor row.
// Retain the older camel-case response member alongside its current wire name.
func (f Factor) MarshalJSON() ([]byte, error) {
	type legacy Factor
	return json.Marshal(struct {
		legacy
		UpdatedAt       any               `json:"updated_at"`
		DeletedAt       any               `json:"deleted_at"`
		ExpiredAt       any               `json:"expired_at"`
		CreatedResponse map[string]string `json:"created_response"`
	}{legacy: legacy(f), UpdatedAt: f.CreatedAt, CreatedResponse: f.CreatedResponse})
}
