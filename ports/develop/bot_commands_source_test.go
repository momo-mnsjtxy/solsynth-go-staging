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

func TestBotCommandSourceAndFixtureIntegrity(t *testing.T) {
	base := "../../inventory/product-client-evidence"
	data, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files []struct {
			File, Blob, SHA256, Repository, Revision string
			Bytes                                    int
		}
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) < 18 {
		t.Fatal("missing evidence")
	}
	for _, entry := range manifest.Files {
		if filepath.Base(entry.File) != entry.File {
			t.Fatal("invalid retained source path")
		}
		raw, err := os.ReadFile(filepath.Join(base, entry.File))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		blob := sha1.New()
		fmt.Fprintf(blob, "blob %d\x00", len(raw))
		blob.Write(raw)
		if hex.EncodeToString(sum[:]) != entry.SHA256 || hex.EncodeToString(blob.Sum(nil)) != entry.Blob || len(raw) != entry.Bytes {
			t.Fatal(entry.File)
		}
		if entry.Repository == "Solsynth/Solian" {
			if entry.Revision != "ffd0838e5f04cb40bea311a39118ebeedad210c1" {
				t.Fatal(entry.Revision)
			}
		} else if entry.Repository != "Solsynth/DysonNetwork" || entry.Revision != "4ee2d0cebb9da8284a92483fee6dce795bf6947c" {
			t.Fatal(entry.Repository, entry.Revision)
		}
	}
	data, err = os.ReadFile(filepath.Join(base, "bot-command-oracle/fixture-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases, Accepted, Rejected int
		Files                     []struct {
			File, SHA256 string
			Bytes        int
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Cases != 19 || fixture.Accepted != 6 || fixture.Rejected != 13 {
		t.Fatal("synthetic fixture count changed")
	}
	for _, entry := range fixture.Files {
		if filepath.Base(entry.File) != entry.File {
			t.Fatal(entry.File)
		}
		raw, err := os.ReadFile(filepath.Join(base, "bot-command-oracle", entry.File))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != entry.SHA256 || len(raw) != entry.Bytes {
			t.Fatal(entry.File)
		}
	}
}

func TestBotCommandOwnedSourceAnchors(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	controller := read("source/DysonNetwork.Develop__Identity__BotAccountPublicController.cs")
	for _, anchor := range []string{`[Route("api/bots")]`, `[HttpGet("{botId:guid}/commands")]`, `return Ok(config?.Commands ?? []);`, `Code = "DEV_BOT_ACCOUNT_NOT_FOUND"`} {
		if !strings.Contains(controller, anchor) {
			t.Fatal(anchor)
		}
	}
	service := read("product-source/DysonNetwork.Develop__Identity__BotAccountService.cs")
	for _, anchor := range []string{`.Include(b => b.Project)`, `.FirstOrDefaultAsync(b => b.Id == id)`, `GetChatConfigOrNullAsync(Guid botId)`, `.FirstOrDefaultAsync(c => c.Id == botId)`} {
		if !strings.Contains(service, anchor) {
			t.Fatal(anchor)
		}
	}
	doc := read("../../inventory/product-client-evidence/DysonNetwork__docs__BOT_CHAT_API.md")
	for _, anchor := range []string{"### SnBotCommand\n", "### SnBotCommandParameter\n", "| `usage` | string? |", "| `parameters` | List\\<SnBotCommandParameter\\>? |", "| `required` | bool |"} {
		if !strings.Contains(doc, anchor) {
			t.Fatal(anchor)
		}
	}
}
