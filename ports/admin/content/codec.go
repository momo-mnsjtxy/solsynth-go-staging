// SPDX-License-Identifier: AGPL-3.0-only
package content

import (
	"net/http"
)

// Shared bounded admin HTTP codec. These functions confer no authorization.
func DecodeStrict(w http.ResponseWriter, r *http.Request, v any) error { return decode(w, r, v) }
func EmptyBody(w http.ResponseWriter, r *http.Request) error           { return empty(w, r) }
func Respond(w http.ResponseWriter, status int, v any)                 { respond(w, status, v) }
func RespondError(w http.ResponseWriter, e error)                      { failure(w, e) }
