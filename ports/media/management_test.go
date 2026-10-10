// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagementJSONRejectsNestedDuplicateFields(t *testing.T) {
	for _, body := range []string{`{"name":"first","name":"second"}`, `{"user_meta":{"x":1,"x":2}}`, `{"name":"ok"} {}`} {
		r := httptest.NewRequest("POST", "/api/files/folders", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var v FilePatch
		if e := decodeManagement(httptest.NewRecorder(), r, &v); e == nil {
			t.Fatal("ambiguous JSON accepted", body)
		}
	}
}
func TestOpaqueEncryptionAndExpiryValidation(t *testing.T) {
	now := time.Now()
	good := Options{EncryptionScheme: "file.aesgcm.v1", EncryptionHeader: base64.StdEncoding.EncodeToString(make([]byte, 16)), EncryptionSignature: base64.StdEncoding.EncodeToString(make([]byte, 64))}
	if e := ValidateOptions(good, now); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Options){func(v *Options) { v.EncryptionHeader = "invalid" }, func(v *Options) { v.EncryptionSignature = "" }, func(v *Options) { past := now.Add(-time.Second); v.ExpiredAt = &past }, func(v *Options) { v.ParentID = "../../folder" }} {
		v := good
		change(&v)
		if ValidateOptions(v, now) == nil {
			t.Fatal("invalid encryption/hierarchy options", v)
		}
	}
}
