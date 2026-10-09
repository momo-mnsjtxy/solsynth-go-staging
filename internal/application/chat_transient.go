// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"solsynth.local/go-migration/internal/realtime"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func placeholderCommands(repo rooms.PlaceholderRepository) realtime.CommandHandler {
	return realtime.CommandHandlerFunc(func(ctx context.Context, actor string, p realtime.Command) (json.RawMessage, error) {
		if repo == nil {
			return nil, rooms.ErrUnavailable
		}
		if p.Endpoint != "DysonNetwork.Messager" {
			return nil, rooms.ErrUnsupported
		}
		var v messager.MessageProjection
		var e error
		switch p.Type {
		case "messages.placeholder.update":
			var input rooms.PlaceholderUpdate
			if e = decodePlaceholder(p.Data, &input, "message_id", "content_chunk", "progress"); e == nil {
				e = rooms.ValidatePlaceholderUpdate(input)
			}
			if e == nil {
				v, e = repo.UpdatePlaceholder(ctx, actor, input)
			}
		case "messages.placeholder.finalize":
			var input rooms.PlaceholderFinalize
			if e = decodePlaceholder(p.Data, &input, "message_id", "content", "attachments_id"); e == nil {
				e = rooms.ValidatePlaceholderFinalize(input)
			}
			if e == nil {
				v, e = repo.FinalizePlaceholder(ctx, actor, input)
			}
		default:
			return nil, rooms.ErrUnsupported
		}
		if e != nil {
			return nil, e
		}
		// The authored finalize packet formats the returned plaintext content;
		// stored content and placeholder chunk metadata remain unchanged.
		v.Content = messager.FormatMessageContent(v.Content, false)
		return json.Marshal(v)
	})
}
func decodePlaceholder(data []byte, out any, allowed ...string) error {
	if len(data) > 32768 {
		return rooms.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return rooms.ErrInvalid
	}
	keys := map[string]bool{}
	for _, k := range allowed {
		keys[k] = true
	}
	seen := map[string]bool{}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return rooms.ErrInvalid
		}
		k, ok := t.(string)
		if !ok || !keys[k] || seen[k] {
			return rooms.ErrInvalid
		}
		seen[k] = true
		var raw json.RawMessage
		if e = d.Decode(&raw); e != nil {
			return rooms.ErrInvalid
		}
	}
	if _, e = d.Token(); e != nil {
		return rooms.ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return rooms.ErrInvalid
	}
	if e = json.Unmarshal(data, out); e != nil {
		return rooms.ErrInvalid
	}
	return nil
}
