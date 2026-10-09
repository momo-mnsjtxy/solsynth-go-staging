// SPDX-License-Identifier: AGPL-3.0-only
package connectionprovider

import (
	"context"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/passport/activities"
	p "solsynth.local/go-migration/ports/passport/connections"
	"strconv"
)

type SteamPresence struct {
	key    string
	client *http.Client
}

func NewSteamPresence(key string, client *http.Client) (*SteamPresence, error) {
	if len(key) < 16 || len(key) > 256 {
		return nil, p.ErrInvalid
	}
	for _, c := range key {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return nil, p.ErrInvalid
		}
	}
	return &SteamPresence{key, restrictedClient(client)}, nil
}
func (s *SteamPresence) Observe(ctx context.Context, id string) (out activities.Content, active bool, e error) {
	if len(id) != 17 {
		return out, false, p.ErrProof
	}
	if _, e = strconv.ParseUint(id, 10, 64); e != nil {
		return out, false, p.ErrProof
	}
	endpoint := "https://api.steampowered.com/ISteamUser/GetPlayerSummaries/v0002/?" + url.Values{"key": {s.key}, "steamids": {id}}.Encode()
	req, e := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if e != nil {
		return
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return out, false, p.ErrUnavailable
	}
	var result struct {
		Response struct {
			Players []struct {
				ID     string `json:"steamid"`
				GameID string `json:"gameid"`
				Game   string `json:"gameextrainfo"`
				Name   string `json:"personaname"`
			} `json:"players"`
		} `json:"response"`
	}
	if e = readJSON(resp, &result); e != nil {
		return
	}
	if len(result.Response.Players) != 1 || result.Response.Players[0].ID != id {
		return out, false, p.ErrProof
	}
	player := result.Response.Players[0]
	if player.GameID == "" || player.Game == "" {
		return out, false, nil
	}
	if _, e = strconv.ParseUint(player.GameID, 10, 64); e != nil {
		return out, false, p.ErrProof
	}
	provider, kind := "steam", "gaming"
	out = activities.Content{Provider: &provider, Type: &kind, ReferenceID: &player.GameID, Title: &player.Game, Subtitle: &player.Name, QueryableTerms: []string{}, Tags: []activities.Tag{}}
	return out, true, nil
}
