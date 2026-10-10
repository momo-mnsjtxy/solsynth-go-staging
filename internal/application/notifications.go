// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"errors"
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

// ProcessNotificationStep runs one finite production worker step, including at
// most one provider delivery after its database lease transaction commits.
func (b *Backend) ProcessNotificationStep(ctx context.Context) (int, error) {
	if b == nil || b.notificationWorker == nil {
		return 0, errors.New("notification worker unavailable")
	}
	return b.notificationWorker.Step(ctx)
}

// ProcessSurveyNotifications performs a bounded local-only step through the same
// projector registered in RunNotifications. It never dispatches external push.
func (b *Backend) ProcessSurveyNotifications(ctx context.Context, limit int) (int, error) {
	if b == nil || b.surveyNotifications == nil {
		return 0, errors.New("survey notification worker unavailable")
	}
	return b.surveyNotifications(ctx, limit)
}

// ProcessCollectionPublicationNotifications runs a bounded local projection step
// through the same durable worker registered by RunNotifications.
func (b *Backend) ProcessCollectionPublicationNotifications(ctx context.Context, limit int) (int, error) {
	if b == nil || b.collectionNotifications == nil {
		return 0, errors.New("collection publication notifications unavailable")
	}
	return b.collectionNotifications(ctx, limit)
}

// ProcessPublisherContentNotifications projects durable post-publication and
// livestream-start intents into the ordinary notification outbox.
func (b *Backend) ProcessPublisherContentNotifications(ctx context.Context, limit int) (int, error) {
	if b == nil || b.publisherNotifications == nil {
		return 0, errors.New("publisher content notifications unavailable")
	}
	return b.publisherNotifications.ProcessDue(ctx, limit)
}
