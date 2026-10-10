// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import "context"

const ChatConnectionsVersion = 1

// ChatConnections is an owned transport hook, not an inferred legacy frame DTO.
// The context must carry the original verified credential proof on every call.
type ChatConnections interface {
	OpenConnection(context.Context, string) (string, error)
	RenewConnection(context.Context, string, string) error
	CloseConnection(context.Context, string, string) error
	SetSubscription(context.Context, string, string, string, bool) error
	CurrentConnectionDevice(context.Context, string, string) (string, error)
}

type chatConnectionContextKey struct{}

func WithChatConnection(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, chatConnectionContextKey{}, id)
}

func ChatConnectionFrom(ctx context.Context) string {
	id, _ := ctx.Value(chatConnectionContextKey{}).(string)
	return id
}
