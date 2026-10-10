// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"

	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type publicationObservers []postpg.PublicationObserver

func (all publicationObservers) RecordPublication(ctx context.Context, db *sql.DB, tx *sql.Tx, p posts.Post) error {
	for _, observer := range all {
		if observer != nil {
			if err := observer.RecordPublication(ctx, db, tx, p); err != nil {
				return err
			}
		}
	}
	return nil
}
