// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"net/http"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

// PublishingSettings is the owned scalar projection evidenced by the licensed
// Sphere EF snapshot and authored caller. Unverified navigation models are not
// exposed. Null request fields retain the stored choice, as in the controller.
type PublishingSettings struct {
	ID                          string     `json:"id"`
	AccountID                   string     `json:"account_id"`
	DefaultPostingPublisherID   *string    `json:"default_posting_publisher_id"`
	DefaultReplyPublisherID     *string    `json:"default_reply_publisher_id"`
	DefaultFediversePublisherID *string    `json:"default_fediverse_publisher_id"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   *time.Time `json:"updated_at"`
	DeletedAt                   *time.Time `json:"deleted_at"`
}
type PublishingSettingsPatch struct {
	DefaultPostingPublisherID   *string `json:"default_posting_publisher_id"`
	DefaultReplyPublisherID     *string `json:"default_reply_publisher_id"`
	DefaultFediversePublisherID *string `json:"default_fediverse_publisher_id"`
}

func (p PublishingSettingsPatch) Validate() error {
	for _, id := range []*string{p.DefaultPostingPublisherID, p.DefaultReplyPublisherID, p.DefaultFediversePublisherID} {
		if id != nil && !authority.ValidID(*id) {
			return ErrInvalid
		}
	}
	return nil
}

type PublishingSettingsRepository interface {
	PublishingSettings(context.Context, string) (PublishingSettings, error)
	UpdatePublishingSettings(context.Context, string, PublishingSettingsPatch) (PublishingSettings, error)
}

var (
	ErrPublishingPublisher  = &Failure{"PUBLISHER_NOT_FOUND", "Publisher not found", 400}
	ErrPublishingMembership = &Failure{"PUBLISHER_NOT_A_MEMBER", "You are not a member of this publisher", 400}
	ErrPublishingFederation = &Failure{"PUBLISHER_FEDIVERSE_NOT_ENABLED", "Fediverse is not enabled for this publisher", 400}
)

func (s *Service) servePublishing(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "PATCH" {
		w.Header().Set("Allow", "GET, PATCH")
		w.WriteHeader(405)
		return
	}
	p, e := s.principal(r)
	if e != nil {
		failure(w, e)
		return
	}
	repo, ok := s.Repository.(PublishingSettingsRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	ctx := accountContext(s.Repository, r.Context(), p)
	var out PublishingSettings
	if r.Method == "GET" {
		out, e = repo.PublishingSettings(ctx, p.Account.ID)
	} else {
		var patch PublishingSettingsPatch
		if e = decode(w, r, &patch); e == nil {
			e = patch.Validate()
		}
		if e == nil {
			out, e = repo.UpdatePublishingSettings(ctx, p.Account.ID, patch)
		}
	}
	if e != nil {
		failure(w, e)
		return
	}
	send(w, 200, out)
}
