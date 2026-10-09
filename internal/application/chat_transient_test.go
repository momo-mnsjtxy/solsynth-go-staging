// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"encoding/json"
	"testing"

	"solsynth.local/go-migration/internal/realtime"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type transientCommandsRepo struct {
	rooms.PlaceholderRepository
	calls int
	actor string
	ctx   context.Context
}

func (r *transientCommandsRepo) FinalizePlaceholder(ctx context.Context, actor string, p rooms.PlaceholderFinalize) (messager.MessageProjection, error) {
	r.calls++
	r.actor = actor
	r.ctx = ctx
	return messager.MessageProjection{ID: p.MessageID, Type: "text", Content: p.Content}, nil
}
func TestPlaceholderCommandsRejectUnownedFieldsAndPreserveContext(t *testing.T) {
	repo := &transientCommandsRepo{}
	h := placeholderCommands(repo)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "proof")
	id := "00000000-0000-4000-8000-000000000001"
	p := realtime.Command{Type: "messages.placeholder.finalize", Endpoint: "DysonNetwork.Messager", Data: json.RawMessage(`{"message_id":"` + id + `","content":"# complete","attachments_id":[]}`)}
	out, e := h.HandleCommand(ctx, "owner", p)
	if e != nil || repo.calls != 1 || repo.actor != "owner" || repo.ctx.Value(key{}) != "proof" || !json.Valid(out) {
		t.Fatal(string(out), e)
	}
	var result messager.MessageProjection
	if e = json.Unmarshal(out, &result); e != nil || result.Content == nil || *result.Content != `\# complete` {
		t.Fatal("finalize packet lost authored heading escaping", string(out), e)
	}
	for _, raw := range []string{`{"message_id":"` + id + `","account_id":"another"}`, `{"message_id":"` + id + `","content":"a","content":"b"}`, `{"message_id":"` + id + `","encryption_meta":{}}`, `null`} {
		bad := p
		bad.Data = json.RawMessage(raw)
		if _, e = h.HandleCommand(ctx, "owner", bad); e == nil {
			t.Fatal(raw)
		}
	}
	for _, change := range []func(*realtime.Command){func(p *realtime.Command) { p.Endpoint = "unknown" }, func(p *realtime.Command) { p.Type = "messages.placeholder.finalize.v2" }} {
		bad := p
		change(&bad)
		if _, e = h.HandleCommand(ctx, "owner", bad); e == nil {
			t.Fatal(bad)
		}
	}
	if repo.calls != 1 {
		t.Fatal("invalid packet reached mutation", repo.calls)
	}
}
