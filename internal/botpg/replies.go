// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"errors"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func replyError(err error) error {
	switch {
	case errors.Is(err, rooms.ErrInvalid):
		return bots.ErrInvalid
	case errors.Is(err, rooms.ErrForbidden):
		return bots.ErrForbidden
	case errors.Is(err, rooms.ErrNotFound):
		return bots.ErrNotFound
	case errors.Is(err, rooms.ErrConflict):
		return bots.ErrConflict
	case errors.Is(err, rooms.ErrUnsupported):
		return bots.ErrUnsupported
	case errors.Is(err, rooms.ErrUnavailable):
		return bots.ErrUnavailable
	default:
		return err
	}
}

// Reply authenticates an owned bot credential and delegates the write to the
// canonical chat repository. It never constructs a user session or developer
// principal, and the chat repository rechecks the key inside its transaction.
func (r *Repository) Reply(ctx context.Context, bot, key string, request bots.ReplyRequest) (messager.MessageProjection, error) {
	if r == nil || r.replies == nil {
		return messager.MessageProjection{}, bots.ErrUnsupported
	}
	if !authority.ValidID(bot) || !authority.ValidID(request.RoomID) || key == "" || len(key) > 256 {
		return messager.MessageProjection{}, bots.ErrInvalid
	}
	if request.EncryptionMeta == nil && (request.DeviceID != "" || request.MLSStateVersion != nil) {
		return messager.MessageProjection{}, bots.ErrInvalid
	}
	if request.EncryptionMeta != nil {
		if !rooms.ValidMLSIdentifier(request.DeviceID) || request.MLSStateVersion == nil || *request.MLSStateVersion < 1 || *request.MLSStateVersion > rooms.MLSMaxEpoch {
			return messager.MessageProjection{}, bots.ErrInvalid
		}
		ctx = rooms.WithMLSWrite(ctx, rooms.MLSWriteContext{Encryption: request.EncryptionMeta, ClientMessageID: request.ClientMessageID, Device: request.DeviceID, Generation: request.MLSStateVersion})
	}
	out, err := r.replies.SendBot(ctx, bot, key, request.RoomID, rooms.TextRequest{Content: request.Content, ContentSet: request.EncryptionMeta == nil, EncryptionMeta: request.EncryptionMeta, RepliedMessageID: request.RepliedMessageID, ClientMessageID: request.ClientMessageID})
	return out, replyError(err)
}
