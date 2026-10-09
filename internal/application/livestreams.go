// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"database/sql"
	"errors"
	"net/http"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type LivestreamConfig struct {
	Provider live.Provider
	Policy   livestreampg.Config
	Chat     *livestreampg.ChatConfig
	Awards   *livestreampg.AwardConfig
	Outputs  *livestreampg.OutputConfig
}

func (x *externalModules) configureStreams(db *sql.DB, c Config, v *authority.Verifier, g *authoritypg.TransactionGuard, a *accountpg.Repository, s *socialpg.Repository, m *mediapg.Repository, commerceRepo *commercepg.Repository) (http.Handler, error) {
	if c.LivestreamAwardMaintenance != nil {
		if c.Livestreams != nil {
			return nil, errors.New("award maintenance mode excludes streaming APIs")
		}
		var err error
		x.awardMaintenance, err = livestreampg.NewAwardMaintenance(db, commerceRepo, *c.LivestreamAwardMaintenance)
		if err != nil {
			return nil, err
		}
	}
	if c.Livestreams == nil {
		return (&live.Service{}).Handler(), nil
	}
	if c.Livestreams.Provider == nil || g == nil {
		return nil, errors.New("livestreams require a configured provider and canonical authority")
	}
	var media live.MediaProvider
	if m != nil {
		media = m
	}
	var err error
	x.streams, err = livestreampg.New(db, g, a, media, s, c.Livestreams.Provider, c.Livestreams.Policy)
	if err != nil {
		return nil, err
	}
	if c.Livestreams.Chat != nil {
		if err = x.streams.WithChat(*c.Livestreams.Chat); err != nil {
			return nil, err
		}
	}
	if c.Livestreams.Outputs != nil {
		var artifacts live.ArtifactStore
		if len(c.Livestreams.Outputs.HLSNamespaces) > 0 {
			var ok bool
			artifacts, ok = c.MediaStore.(live.ArtifactStore)
			if !ok || c.Livestreams.Outputs.MaxSegmentBytes > c.MaxMediaBytes {
				return nil, errors.New("HLS requires the configured canonical private artifact store")
			}
		}
		if err = x.streams.WithOutputs(*c.Livestreams.Outputs, artifacts); err != nil {
			return nil, err
		}
	}
	if c.Livestreams.Awards != nil {
		if err = x.streams.WithAwards(*c.Livestreams.Awards, commerceRepo); err != nil {
			return nil, err
		}
		commerceRepo.WithLiveStreamAwards(x.streams)
	}
	return (&live.Service{Repository: x.streams, Authority: v, Bind: g.Bind}).Handler(), nil
}
