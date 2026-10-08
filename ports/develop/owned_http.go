// SPDX-License-Identifier: AGPL-3.0-only
package develop

import "net/http"

// NewOwnedHandler provides public discovery with a canonical owned Reader. The
// embedding host verifies any supplied account credentials before dispatching;
// account identity neither widens visibility nor changes the discovery query.
func NewOwnedHandler(reader Reader) http.Handler {
	return discoveryHandler{reader: reader, candidate: true, owned: true}
}
