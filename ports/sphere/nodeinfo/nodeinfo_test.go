// SPDX-License-Identifier: AGPL-3.0-only
package nodeinfo

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverySourceLinkOrderAndDomainFallback(t *testing.T) {
	for _, value := range []string{"social.example", "", "localhost:8443", "a&b.example"} {
		for _, d := range []*string{&value, nil} {
			wantDomain := value
			if d == nil {
				wantDomain = "localhost"
			}
			got := Discover(d)
			if len(got.Links) != 2 {
				t.Fatal(got)
			}
			for i, version := range []string{"2.1", "2.0"} {
				if got.Links[i].Rel != "http://nodeinfo.diaspora.software/ns/schema/"+version || got.Links[i].Href != "https://"+wantDomain+"/nodeinfo/"+version {
					t.Fatal(got)
				}
			}
		}
	}
}

// Full expected documents record the source-owned anonymous DTO and Sphere's
// SnakeCaseLower policy. These are source-derived fixtures, not .NET captures.
const commonExpected = `"software":{"name":"dysonnetwork","version":"1.0.0"},"protocols":["activitypub"],"services":{"inbound":[],"outbound":["activitypub"]},"usage":{"users":{"total":7,"active_month":3,"active_halfyear":3},"local_posts":11,"local_comments":0},"open_registrations":true,`
const configExpected = `"accounts":{"max_featured_objects":10},"statuses":{"max_characters":5000,"max_media_attachments":4},"media_attachments":{"supported_mime_types":["image/*","video/*","audio/*"],"image_size_limit":10000000,"image_matrix_limit":4000000,"video_size_limit":40000000,"video_frame_rate_limit":30,"video_matrix_limit":2000000}`

func compareJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestCompleteSourcePayloads(t *testing.T) {
	stats := Stats{TotalPublishers: 7, ActivePublishers: 3, TotalPosts: 11}
	for _, tc := range []struct {
		doc  Document
		want string
	}{
		{Version20(stats), `{` + commonExpected + `"configuration":{"urls":{},` + configExpected + `}}`},
		{Version21(stats), `{"version":"2.1",` + commonExpected + `"configuration":{` + configExpected + `},"metadata":{}}`},
	} {
		b, err := json.Marshal(tc.doc)
		if err != nil {
			t.Fatal(err)
		}
		compareJSON(t, b, tc.want)
	}
}

func TestFreshDocumentsAndInt32Boundaries(t *testing.T) {
	a := Version20(Stats{})
	a.Protocols[0] = "changed"
	a.Services.Outbound[0] = "changed"
	a.Configuration.MediaAttachments.SupportedMIMETypes[0] = "changed"
	b := Version20(Stats{TotalPublishers: math.MaxInt32, ActivePublishers: math.MaxInt32, TotalPosts: math.MaxInt32})
	if b.Protocols[0] != "activitypub" || b.Services.Outbound[0] != "activitypub" || b.Configuration.MediaAttachments.SupportedMIMETypes[0] != "image/*" || b.Services.Inbound == nil {
		t.Fatal(b)
	}
	data, err := json.Marshal(b)
	if err != nil || strings.Count(string(data), "2147483647") != 4 {
		t.Fatal(string(data), err)
	}
}

func TestPinnedSourceAndLicenseEvidence(t *testing.T) {
	b, err := os.ReadFile("source/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct{ File, Path, Blob, Revision, SHA256, URL, License string }
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("source evidence count %d", len(entries))
	}
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join("source", entry.File))
		if err != nil {
			t.Fatal(err)
		}
		sha := sha256.Sum256(b)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...))
		if hex.EncodeToString(sha[:]) != entry.SHA256 || hex.EncodeToString(blob[:]) != entry.Blob || entry.License != "AGPL-3.0-only" || entry.Revision != "4ee2d0cebb9da8284a92483fee6dce795bf6947c" || entry.URL != "https://github.com/Solsynth/DysonNetwork/blob/"+entry.Revision+"/"+entry.Path {
			t.Fatalf("invalid pinned evidence: %s", entry.File)
		}
	}
}
