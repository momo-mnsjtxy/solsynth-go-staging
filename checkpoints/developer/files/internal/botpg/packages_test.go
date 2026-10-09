// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
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

func TestPackageRejectsSymlinkDuplicateCRCAndExpansion(t *testing.T) {
	manifest := `{"id":"test.synthetic.plugin","name":"Synthetic"}`
	for _, kind := range []string{"symlink", "duplicate", "expanded", "count", "crc"} {
		t.Run(kind, func(t *testing.T) {
			var b bytes.Buffer
			w := zip.NewWriter(&b)
			p, _ := w.Create("manifest.json")
			p.Write([]byte(manifest))
			switch kind {
			case "symlink":
				h := &zip.FileHeader{Name: "link"}
				h.SetMode(os.ModeSymlink | 0600)
				p, _ = w.CreateHeader(h)
				p.Write([]byte("target"))
			case "duplicate":
				p, _ = w.Create("data")
				p.Write([]byte("one"))
				p, _ = w.Create("data")
				p.Write([]byte("two"))
			case "expanded":
				p, _ = w.Create("huge")
				io.Copy(p, io.LimitReader(zeroReader{}, (32<<20)+1))
			case "count":
				for i := 0; i < 1024; i++ {
					w.Create(fmt.Sprint("item", i))
				}
			case "crc":
				h := &zip.FileHeader{Name: "data", Method: zip.Store}
				p, _ = w.CreateHeader(h)
				p.Write([]byte("unique corruption sentinel"))
			}
			w.Close()
			data := b.Bytes()
			if kind == "crc" {
				index := bytes.Index(data, []byte("unique corruption sentinel"))
				if index < 0 {
					t.Fatal("fixture")
				}
				data[index] ^= 0xff
			}
			if _, e := ReadPackageManifest(data); e == nil {
				t.Fatal("unsafe package accepted")
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestPackageRejectsUnderstatedCentralDirectoryCount(t *testing.T) {
	data := packageZip(t, map[string]string{"manifest.json": `{"id":"test.synthetic.package","name":"Synthetic"}`, "other": "inert"})
	end := len(data) - 22
	binary.LittleEndian.PutUint16(data[end+8:end+10], 1)
	binary.LittleEndian.PutUint16(data[end+10:end+12], 1)
	if _, e := ReadPackageManifest(data); e == nil {
		t.Fatal("central count lie accepted")
	}
}

func TestPackageDirectoryPrepassRejectsShadowEndRecord(t *testing.T) {
	for _, trailing := range []int{1, 1025} {
		t.Run(fmt.Sprint("trailing-", trailing), func(t *testing.T) {
			data := packageZip(t, map[string]string{"manifest.json": `{"id":"test.synthetic.package","name":"Synthetic"}`})
			end := len(data) - 22
			directory := int(binary.LittleEndian.Uint32(data[end+16 : end+20]))
			shadow := bytes.Repeat(data[directory:end], 1025)
			fakeEnd := append([]byte(nil), data[end:]...)
			binary.LittleEndian.PutUint16(fakeEnd[8:10], 1025)
			binary.LittleEndian.PutUint16(fakeEnd[10:12], 1025)
			binary.LittleEndian.PutUint32(fakeEnd[12:16], uint32(len(shadow)))
			binary.LittleEndian.PutUint32(fakeEnd[16:20], uint32(len(data)))
			commentSize := len(shadow) + len(fakeEnd) + trailing
			if commentSize > 65535 {
				t.Fatal("fixture exceeds ZIP comment bound")
			}
			binary.LittleEndian.PutUint16(data[end+20:end+22], uint16(commentSize))
			data = append(data, shadow...)
			data = append(data, fakeEnd...)
			data = append(data, make([]byte, trailing)...)
			// Go selects the later record even with bytes after its empty comment.
			// Prove the fixture reaches the otherwise unchecked allocation path.
			archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil || len(archive.File) != 1025 {
				t.Fatal("shadow-directory fixture", err)
			}
			if boundedZipDirectory(bytes.NewReader(data), int64(len(data))) {
				t.Fatal("prepass validated an end record other than archive/zip's")
			}
			if _, err = ReadPackageManifest(data); err == nil {
				t.Fatal("shadow directory accepted")
			}
		})
	}
}

func TestPackageRequiresOrdinaryExactEntryAndUnambiguousNames(t *testing.T) {
	manifest := `{"id":"test.synthetic.package","name":"Synthetic"}`
	for name, files := range map[string]map[string]string{
		"missing-entry":      {"manifest.json": manifest},
		"entry-is-directory": {"manifest.json": manifest, "main.js/": ""},
		"case-fold-alias":    {"manifest.json": manifest, "main.js": "one", "MAIN.JS": "two"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadPackageManifest(packageZip(t, files)); err == nil {
				t.Fatal("ambiguous or missing entry accepted")
			}
		})
	}

	data := packageZip(t, map[string]string{"manifest.json": manifest, "main.js": "inert"})
	name := []byte("main.js")
	index := bytes.Index(data, name)
	if index < 0 {
		t.Fatal("local-name fixture")
	}
	copy(data[index:index+len(name)], []byte("Main.js"))
	if _, err := ReadPackageManifest(data); err == nil {
		t.Fatal("central/local filename mismatch accepted")
	}
}
