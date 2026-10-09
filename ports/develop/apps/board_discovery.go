// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"context"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/passport/board"
	"strconv"
	"strings"
)

type BoardDiscoveryQuery struct {
	Take, Offset int
	Slug         string
}
type BoardDiscoveryWidget struct {
	Key             string            `json:"key"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Slug            string            `json:"slug"`
	IsEnabled       bool              `json:"is_enabled"`
	RendererType    string            `json:"renderer_type"`
	FieldTypes      []board.FieldType `json:"field_types"`
	RequiredFields  []string          `json:"required_fields"`
	MaxPayloadBytes *int              `json:"max_payload_bytes"`
	AllowMultiple   bool              `json:"allow_multiple"`
}
type BoardDiscoveryApp struct {
	ID            string                 `json:"id"`
	Slug          string                 `json:"slug"`
	Name          string                 `json:"name"`
	Description   *string                `json:"description"`
	PublisherName string                 `json:"publisher_name"`
	BoardWidgets  []BoardDiscoveryWidget `json:"board_widgets"`
}
type BoardDiscoveryRepository interface {
	DiscoverBoard(context.Context, BoardDiscoveryQuery) ([]BoardDiscoveryApp, error)
}

func ValidateBoardDiscovery(q BoardDiscoveryQuery) error {
	if q.Take < 0 || q.Take > 100 || q.Offset < 0 || q.Offset > 100000 || len(q.Slug) > 128 {
		return ErrInvalid
	}
	return nil
}
func (s *Service) discoverBoard(w http.ResponseWriter, r *http.Request, values url.Values) {
	q := BoardDiscoveryQuery{Take: 20, Slug: strings.TrimSpace(values.Get("slug"))}
	for _, v := range []struct {
		name string
		out  *int
	}{{"take", &q.Take}, {"offset", &q.Offset}} {
		if raw, ok := values[v.name]; ok {
			n, e := strconv.Atoi(raw[0])
			if e != nil {
				failure(w, ErrInvalid)
				return
			}
			*v.out = n
		}
	}
	if e := ValidateBoardDiscovery(q); e != nil {
		failure(w, e)
		return
	}
	repo, ok := s.Repository.(BoardDiscoveryRepository)
	if !ok {
		failure(w, ErrUnavailable)
		return
	}
	out, e := repo.DiscoverBoard(r.Context(), q)
	if e != nil {
		failure(w, e)
		return
	}
	if out == nil {
		out = []BoardDiscoveryApp{}
	}
	respond(w, 200, out)
}
