// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSubscriptionSyncRetainedEvidenceIntegrity(t *testing.T) {
	for p, want := range map[string]string{
		"ports/messager/source/DysonNetwork.Messager__Chat__ChatController.cs":      "22d8a19becdab64fefa234d0213591f49ab3d08458c186b4900609bb02d38c98",
		"ports/messager/source/DysonNetwork.Messager__Chat__ChatRoomController.cs":  "fbf9898e12c73cb47568035a168203243cab6dbcdd7545cf02861967d6d1d41e",
		"internal/messagerpg/source/ChatRoomService.cs":                             "b366429d359958219f8a55172bfb8d8eb054f9115a5a8aaad5cf48cc3dc744ef",
		"inventory/product-client-evidence/Solian__lib__chat__pods__chat_room.dart": "e4eb3a1c84a8029c549c8525bc17aa399f1ad307c25bbbbcc01aaa5263277984",
		"ports/messager/source/LICENSE.txt":                                         "0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0",
		"internal/messagerpg/source/LICENSE.txt":                                    "0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0",
	} {
		b, e := os.ReadFile(filepath.Join("..", "..", "..", p))
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			t.Fatalf("retained source changed: %s: %v", p, e)
		}
	}
}
