// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func packageZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		v, e := w.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = v.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestPackageStructureManifestAndBounds(t *testing.T) {
	manifest := `{"Id":"test.synthetic.plugin","Name":"Synthetic plugin","entryUrl":"https://example.invalid/main.js"}`
	data := packageZip(t, map[string]string{"manifest.json": manifest, "main.js": "// Synthetic package fixture; never executed."})
	m, e := ReadPackageManifest(data)
	if e != nil || m.ID != "test.synthetic.plugin" || m.Entry != "main.js" {
		t.Fatal(m, e)
	}
	for name, files := range map[string]map[string]string{"missing": {"main.js": "x"}, "multiple": {"manifest.json": manifest, "nested/manifest.json": manifest}, "traversal": {"../manifest.json": manifest}, "windows": {"C:/manifest.json": manifest}, "duplicate_alias": {"manifest.json": `{"id":"test.synthetic.plugin","ID":"test.other.plugin","name":"Synthetic"}`}, "invalid_manifest": {"manifest.json": `{"id":"single","name":"Synthetic"}`}, "manifest_bound": {"manifest.json": strings.Repeat(" ", 16385)}, "entry_url": {"manifest.json": `{"id":"test.synthetic.plugin","name":"Synthetic","entryUrl":"javascript:invalid"}`}} {
		t.Run(name, func(t *testing.T) {
			if _, e := ReadPackageManifest(packageZip(t, files)); e == nil {
				t.Fatal("invalid package accepted")
			}
		})
	}
	if _, e := ReadPackageManifest([]byte("not a zip")); e == nil {
		t.Fatal("MIME cannot validate bytes")
	}
	if _, e := ReadPackageManifest(make([]byte, maxPackageBytes+1)); e == nil {
		t.Fatal("package size limit")
	}
}
