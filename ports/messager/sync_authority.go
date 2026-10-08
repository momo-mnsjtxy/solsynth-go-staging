// SPDX-License-Identifier: AGPL-3.0-only
package messager

import "net/http"

// SyncIdentity consumes a host-bound proof, not a client account header. Account
// must revalidate current authority state. Empty is allowed only for evidenced
// public message reads; the repository still enforces room visibility.
type SyncIdentity interface {
	Account(*http.Request) (string, error)
}

func NewAuthoritySyncHandler(reader SyncReader, identity SyncIdentity) http.Handler {
	if identity == nil {
		return NewSyncHandler(NewSyncService(reader))
	}
	return syncHandler{service: &SyncService{reader: reader, synthetic: true}, identity: identity}
}
func fixtureHeaders(synthetic bool, r *http.Request) http.Header {
	if synthetic {
		return r.Header
	}
	return nil
}
func (h syncHandler) projectionVersion() string {
	if h.identity != nil {
		return "owned-authority-room-sync-projection-v1"
	}
	return SyncProjectionVersion
}
