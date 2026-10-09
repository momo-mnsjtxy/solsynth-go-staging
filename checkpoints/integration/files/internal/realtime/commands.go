// SPDX-License-Identifier: AGPL-3.0-only
package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/notifications"
)

// Commands receive the original verified connection context, never an identity
// from packet fields. Implementations must reauthorize every mutation in SQL.
type Command struct {
	Type     string          `json:"type"`
	Endpoint string          `json:"endpoint"`
	Data     json.RawMessage `json:"data"`
}
type CommandHandler interface {
	HandleCommand(context.Context, string, Command) (json.RawMessage, error)
}
type CommandHandlerFunc func(context.Context, string, Command) (json.RawMessage, error)

func (f CommandHandlerFunc) HandleCommand(ctx context.Context, actor string, p Command) (json.RawMessage, error) {
	return f(ctx, actor, p)
}
func decodeCommand(data []byte) (Command, error) {
	var p Command
	if len(data) > 32768 || !utf8.Valid(data) {
		return p, notifications.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return p, notifications.ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return p, notifications.ErrInvalid
		}
		k, ok := t.(string)
		if !ok || seen[k] {
			return p, notifications.ErrInvalid
		}
		seen[k] = true
		var b json.RawMessage
		if e = d.Decode(&b); e != nil {
			return p, notifications.ErrInvalid
		}
		switch k {
		case "type":
			e = json.Unmarshal(b, &p.Type)
		case "endpoint":
			e = json.Unmarshal(b, &p.Endpoint)
		case "data":
			p.Data = b
		case "error_message", "errorMessage":
			if !bytes.Equal(b, []byte("null")) {
				e = notifications.ErrInvalid
			}
		default:
			e = notifications.ErrInvalid
		}
		if e != nil {
			return p, notifications.ErrInvalid
		}
	}
	if _, e = d.Token(); e != nil {
		return p, notifications.ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return p, notifications.ErrInvalid
	}
	if p.Type == "" {
		return p, notifications.ErrInvalid
	}
	return p, nil
}
