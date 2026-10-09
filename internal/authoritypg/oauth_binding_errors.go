// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/authority"
)

func bindingLockError(e error) error {
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "55P03" {
		return authority.ErrUnavailable
	}
	return authority.ErrInvalid
}
