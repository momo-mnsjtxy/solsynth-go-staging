// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestChatOrganizationRetainedEvidenceIntegrity(t *testing.T) {
	for name, want := range map[string]string{
		"ports/messager/contract-evidence/lib__chat__messages_notifier.dart":           "5240bc641d6f31984b960b88c053037799068371e34127882fed6a55ee3859c5",
		"inventory/operation-sources/DysonNetwork.Messager/Chat/ChatController.cs":     "22d8a19becdab64fefa234d0213591f49ab3d08458c186b4900609bb02d38c98",
		"inventory/operation-sources/DysonNetwork.Messager/Chat/ChatRoomController.cs": "fbf9898e12c73cb47568035a168203243cab6dbcdd7545cf02861967d6d1d41e",
		"ports/messager/source/ChatService.cs":                                         "75a18068893f878c7a5d24529d88415e632f0947894ceea971620ebf1558fb9a",
		"ports/messager/source/DysonNetwork.Messager__Chat__ChatPinService.cs":         "054790088d92dc10828acd01095497922b16e48ab67e48576b98c9e63eb7b6e2",
		"ports/messager/source/DysonNetwork.Messager__Models__ChatMessagePin.cs":       "a440d1af63d155fbcd99da0cf50d502d35f085f16b8a655e680f66a0d98c8e6a",
		"internal/messagerpg/source/AppDatabaseModelSnapshot.cs":                       "7968bb5449b4fa25f30ae3a5f7588e2bd851f39a6c7c70a68944b6cbbcfbbdd7",
		"ports/messager/contract-evidence/lib__chat__realtime__message_handler.dart":   "7cb906dd11414186348cafb5e99fba75d7d1e3972674f44a359152f8e0a99263",
		"ports/messager/source/LICENSE.txt":                                            "0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0",
		"internal/messagerpg/source/LICENSE.txt":                                       "0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0",
	} {
		b, e := os.ReadFile(filepath.Join("..", "..", "..", name))
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			t.Fatalf("retained chat organization source changed: %s: %v", name, e)
		}
	}
}
