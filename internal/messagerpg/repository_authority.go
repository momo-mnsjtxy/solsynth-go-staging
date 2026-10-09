// SPDX-License-Identifier: AGPL-3.0-only
package messagerpg

import (
	"database/sql"
	"solsynth.local/go-migration/ports/messager"
	"time"
)

// IndependentPolicy must explicitly match the database's configured numeric
// enums. No excluded shared enum or production mapping is assumed.
type IndependentPolicy struct {
	NoneEncryptionMode                    int32
	NotifyAll, NotifyMentions, NotifyNone int32
}

func NewWithIndependentPolicy(db *sql.DB, p IndependentPolicy) (*Repository, error) {
	if p.NotifyAll == p.NotifyMentions || p.NotifyAll == p.NotifyNone || p.NotifyMentions == p.NotifyNone {
		return nil, messager.ErrSyncInvalidRequest
	}
	return &Repository{db: db, synthetic: true, owned: true, noneEncryptionMode: p.NoneEncryptionMode, now: time.Now, unreadPolicy: &SyntheticUnreadPolicy{All: p.NotifyAll, Mentions: p.NotifyMentions, None: p.NotifyNone}}, nil
}
