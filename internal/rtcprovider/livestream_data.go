// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored against the public RoomService SendData documentation.
package rtcprovider

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

var _ live.DataProvider = (*LiveKit)(nil)

func (p *LiveKit) SendStreamData(ctx context.Context, room string, recipients []string, payload []byte) error {
	if p == nil {
		return live.ErrUnconfigured
	}
	if !live.Text(room, 256) || room == "" || len(recipients) < 1 || len(recipients) > 100 || len(payload) == 0 || len(payload) > live.MaxDataPacketBytes || !utf8.Valid(payload) || !json.Valid(payload) {
		return live.ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range recipients {
		if !authority.ValidID(id) || seen[id] {
			return live.ErrInvalid
		}
		seen[id] = true
	}
	// Empty destinations mean broadcast in LiveKit and are deliberately forbidden.
	// The server acknowledges acceptance only; reliable packets have no offline buffer.
	if e := p.request(ctx, "SendData", room, map[string]any{"room": room, "data": payload, "kind": "RELIABLE", "destination_identities": recipients}, nil, false); e != nil {
		return live.ErrProvider
	}
	return nil
}
