// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
)

// TextMessageObserver persists delivery work inside the original message
// transaction. It must never commit, transmit data, or acquire domain locks in
// the reverse order. Any error rolls back both message and observer work.
type TextMessageObserver interface {
	QueueMessage(context.Context, *sql.Tx, string, string, string) error
}

// WithTextMessageObserver is an initialization-only host dependency.
func (r *Repository) WithTextMessageObserver(observer TextMessageObserver) *Repository {
	r.textObserver = observer
	return r
}
