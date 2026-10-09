// SPDX-License-Identifier: AGPL-3.0-only
package interactions

import (
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectionSubscriptionNavigationImmutableEvidence(t *testing.T) {
	for _, v := range []struct{ path, sha256, blob string }{
		{"inventory/operation-sources/DysonNetwork.Sphere/Post/PostCollectionController.cs", "4e7e65ad975d7e6a69999b6be70d88ef433a28d08275651c7295b38b60376a49", "a6c11a5b40849986ea250546c7eb6d1e98849b86"},
		{"ports/sphere/interactions/source/PostService.cs", "a8afd1bec596450dea5fa17bbaead101a6c4c8b2a6e799d21323165b0dafa333", "dbacff11ded248680bc34d9fe422cc7e38c49068"},
		{"ports/sphere/interactions/source/PostCollectionService.cs", "2b60e41dfe5b0c8c4a0d9f4efc053e4e06b5bbc22b578c09e2f1eef251c15804", "8b7c3db1925c6cd8f3e6fc91ec06733df3d34571"},
		{"ports/sphere/nodeinfo/source/DysonNetwork.Sphere__Migrations__AppDatabaseModelSnapshot.cs", "1516fbd348413b0c0cd8b102bf5bbbf454032f1874f5c09558a370bfa271fcf6", "97288c526bb8dd7c120fd9157d56069663487b4b"},
		{"ports/sphere/timeline/subscription-source/PublisherSubscriptionService.cs", "949074ab7111f04cf4044de6a65d7457b0f9f7915b33842de68a480455312c23", "e1df7a8168b098b0cb166c626ebc7cc83dc339d5"},
		{"ports/sphere/timeline/subscription-source/LICENSE.txt", "0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0", "be3f7b28e564e7dd05eaf59d64adba1a4065ac0e"},
	} {
		b, e := os.ReadFile(filepath.Join("..", "..", "..", v.path))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != v.sha256 || fmt.Sprintf("%x", sha1.Sum(append([]byte(fmt.Sprintf("blob %d%c", len(b), 0)), b...))) != v.blob {
			t.Fatal("immutable source bytes changed", v.path)
		}
	}
}
