// SPDX-License-Identifier: AGPL-3.0-only
package realmboosts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedAuthoredEvidence(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for p, want := range map[string]string{
		"ports/passport/realmboosts/source/RealmExperienceService.cs":                    "53fa47d2821d1e132755e109823e50f2c2c7f26a2bd367f9a31eb38790cbc907",
		"ports/passport/realmboosts/source/LICENSE.txt":                                  "0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0",
		"inventory/operation-sources/DysonNetwork.Passport/Realm/RealmController.cs":     "b325fdea25d5d4f191fc465e7dff36a9c6289c57d8909046a1c6a63806e9bc74",
		"ports/passport/social/contract-evidence/RealmService.cs":                        "2cc78429c22d3c0f3d4e32b783dccb8e818c1fe163a6827021c732430505c26e",
		"ports/passport/artwork/source/ServiceCollectionExtensions.cs":                   "a421aa8599091ea33a741297a663c2439ef765bc82a64e2289e966c08a4a91c6",
		"inventory/identity-client-evidence/solian/lib/realms/screens/realm_detail.dart": "464be7087a07ee95d3f50da06db3ca0c7cc488218f9579360d88c222b8e214e9",
	} {
		raw, e := os.ReadFile(filepath.Join(root, p))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatal("pinned source changed", p)
		}
	}
}
