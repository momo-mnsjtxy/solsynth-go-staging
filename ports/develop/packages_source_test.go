// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageSourceEvidenceIntegrity(t *testing.T) {
	type entry struct{ File, Path, Blob, Repository, Revision, SHA256 string }
	for _, directory := range []struct {
		name  string
		count int
	}{{"package-source", 4}, {"product-source", 6}} {
		manifest, err := os.ReadFile(filepath.Join(directory.name, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var records []entry
		if err = json.Unmarshal(manifest, &records); err != nil {
			t.Fatal(err)
		}
		if len(records) != directory.count {
			t.Fatalf("%s evidence count=%d", directory.name, len(records))
		}
		for _, record := range records {
			if filepath.Base(record.File) != record.File {
				t.Fatal("nonlocal evidence file")
			}
			wantRevision := "4ee2d0cebb9da8284a92483fee6dce795bf6947c"
			if record.Repository == "Solsynth/Solian" {
				wantRevision = "ffd0838e5f04cb40bea311a39118ebeedad210c1"
			} else if record.Repository != "Solsynth/DysonNetwork" {
				t.Fatal(record.Repository)
			}
			if record.Revision != wantRevision {
				t.Fatal(record.Revision)
			}
			data, err := os.ReadFile(filepath.Join(directory.name, record.File))
			if err != nil {
				t.Fatal(err)
			}
			sha := sha256.Sum256(data)
			blob := sha1.New()
			fmt.Fprintf(blob, "blob %d\x00", len(data))
			blob.Write(data)
			if hex.EncodeToString(sha[:]) != record.SHA256 || hex.EncodeToString(blob.Sum(nil)) != record.Blob {
				t.Fatalf("source/license integrity: %s", record.File)
			}
		}
	}
	// Pin the already-retained controller/schema inputs independently too.
	for file, want := range map[string]string{
		"source/DysonNetwork.Develop__MiniApp__MiniAppPublicController.cs":     "a1c12172fa27bf9040b4ffcf9a8140724c732d9175e9f3e5fc5f259aea231abe",
		"source/DysonNetwork.Develop__Migrations__AppDatabaseModelSnapshot.cs": "dab8948a3da3026491fa4fdc9c9204fdd850ee174051c9c56f61d604d99c2f2a",
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatal(file)
		}
	}
}

func TestPackageSourceContractAnchors(t *testing.T) {
	read := func(file string) string {
		t.Helper()
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	controller := read("source/DysonNetwork.Develop__MiniApp__MiniAppPublicController.cs")
	begin := strings.Index(controller, "public async Task<ActionResult> DownloadPackage")
	if begin < 0 {
		t.Fatal("source download method absent")
	}
	controller = controller[begin:]
	last := -1
	for _, anchor := range []string{
		"GetPublishedMiniAppBySlugAsync(slug)",
		"string.IsNullOrWhiteSpace(miniApp.PackageStorageKey)",
		"IncrementDownloadCountAsync(miniApp.Id)",
		"return Redirect(miniApp.PackageUrl)",
		"GetPackageStreamAsync(miniApp.PackageStorageKey)",
		"return File(stream, \"application/zip\", $\"{miniApp.Slug}.zip\")",
	} {
		at := strings.Index(controller, anchor)
		if at <= last {
			t.Fatal("source sequence changed", anchor)
		}
		last = at
	}
	service := read("package-source/DysonNetwork.Develop__MiniApp__MiniAppService.cs")
	for _, anchor := range []string{"m.Stage == MiniAppStage.Production", "m.Slug.ToLower() == slug.ToLowerInvariant()", ".ThenInclude(p => p.Developer)", ".Where(m => m.Id == id)", ".ExecuteUpdateAsync(s => s.SetProperty(m => m.DownloadCount, m => m.DownloadCount + 1))"} {
		if !strings.Contains(service, anchor) {
			t.Fatal(anchor)
		}
	}
	model := read("package-source/DysonNetwork.Develop__Models__MiniApp.cs")
	if !strings.Contains(model, "public enum MiniAppStage\n{\n    Development,\n    Staging,\n    Production\n}") {
		t.Fatal("owned stage declaration changed")
	}
}
