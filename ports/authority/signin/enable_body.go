// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"bytes"
	"io"
	"net/http"
)

func decodeEnableBody(w http.ResponseWriter, r *http.Request) (string, bool, error) {
	if r.Body == nil {
		return "", false, nil
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if e != nil {
		return "", false, ErrInput
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return "", false, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	var code string
	e = decode(w, r, &code)
	return code, true, e
}
