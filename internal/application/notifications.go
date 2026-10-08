// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"net/http"
)

type notificationIdentity struct{ identity }

func (i notificationIdentity) Account(r *http.Request) (string, error) {
	p, e := i.require(r.Context())
	if e != nil {
		return "", e
	}
	return p.Account.ID, nil
}
func (b *Backend) RunNotifications(ctx context.Context, onError func(string)) error {
	return b.notificationWorker.Run(ctx, onError)
}
