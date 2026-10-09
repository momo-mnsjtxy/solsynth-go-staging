// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"bytes"
	"context"
	"encoding/base64"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"testing"
	"time"
)

type replyCapture struct{ request rooms.TextRequest }

func (c *replyCapture) SendBot(_ context.Context, _, _, _ string, request rooms.TextRequest) (messager.MessageProjection, error) {
	c.request = request
	return messager.MessageProjection{}, nil
}

func TestBotKeyDigestIsBoundToBotAndTimeoutIsBounded(t *testing.T) {
	token := "bk1.00000000-0000-4000-8000-000000000001.synthetic"
	one := botKeyDigest("00000000-0000-4000-8000-000000000002", token)
	two := botKeyDigest("00000000-0000-4000-8000-000000000003", token)
	if bytes.Equal(one, two) || len(one) != 32 {
		t.Fatal("bot credential digest is not resource-bound")
	}
	r := &Repository{}
	for _, invalid := range []time.Duration{0, time.Second, 16 * time.Minute} {
		if r.ConfigurePackageUploadTimeout(invalid) == nil {
			t.Fatal("unbounded package timeout", invalid)
		}
	}
	if err := r.ConfigurePackageUploadTimeout(90 * time.Second); err != nil || r.packageUploadTimeout != 90*time.Second {
		t.Fatal("valid package timeout", err)
	}
}

func TestEncryptedBotReplyPreservesOpaqueRequest(t *testing.T) {
	capture := &replyCapture{}
	repository := (&Repository{}).WithReplySender(capture)
	version := int64(1)
	opaque := base64.StdEncoding.EncodeToString([]byte("opaque"))
	meta := &rooms.EncryptionMeta{StateVersion: &version, Ciphertext: opaque, Header: opaque, Scheme: "chat.mls.v2", Epoch: 0}
	_, err := repository.Reply(context.Background(), "00000000-0000-4000-8000-000000000001", "synthetic-key", bots.ReplyRequest{RoomID: "00000000-0000-4000-8000-000000000002", EncryptionMeta: meta, DeviceID: "bot-device", MLSStateVersion: &version})
	if err != nil || capture.request.EncryptionMeta != meta || capture.request.ContentSet || capture.request.Content != "" {
		t.Fatal("opaque reply changed into plaintext", capture.request, err)
	}
}
