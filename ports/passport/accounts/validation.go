// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

var publisherName = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9._-]*[a-zA-Z0-9])?$`)

func ValidateProfile(p ProfilePatch) error {
	for _, v := range []struct {
		p *string
		n int
	}{{p.Name, 100}, {p.Nick, 256}, {p.Language, 32}, {p.Region, 32}, {p.Bio, 4096}, {p.Pronouns, 128}, {p.Location, 256}, {p.TimeZone, 128}, {p.FirstName, 128}, {p.MiddleName, 128}, {p.LastName, 128}, {p.Gender, 128}} {
		if v.p != nil && (!utf8.ValidString(*v.p) || utf8.RuneCountInString(*v.p) > v.n || strings.ContainsRune(*v.p, 0)) {
			return ErrInvalid
		}
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		return ErrInvalid
	}
	if p.Name != nil {
		return ErrNameChange
	}
	if p.Birthday != nil && (p.Birthday.Year() < 1900 || p.Birthday.Year() > 2199) {
		return ErrInvalid
	}
	if len(p.UsernameColor) > 0 && !bytes.Equal(bytes.TrimSpace(p.UsernameColor), []byte("null")) {
		var color struct {
			Type      string   `json:"type"`
			Value     *string  `json:"value"`
			Direction *string  `json:"direction"`
			Colors    []string `json:"colors"`
		}
		d := json.NewDecoder(bytes.NewReader(p.UsernameColor))
		d.DisallowUnknownFields()
		if d.Decode(&color) != nil {
			return ErrInvalid
		}
		if color.Type != "plain" || color.Value != nil && *color.Value != "" || color.Direction != nil && *color.Direction != "" || len(color.Colors) > 0 {
			return ErrUnsupported
		}
	}
	if p.PictureID != nil || p.BackgroundID != nil {
		return ErrUnsupported
	}
	if p.Nick != nil && strings.TrimSpace(*p.Nick) == "" {
		return ErrInvalid
	}
	if p.TimeZone != nil && *p.TimeZone != "" {
		if *p.TimeZone == "Local" {
			return ErrInvalid
		}
		if _, e := time.LoadLocation(*p.TimeZone); e != nil {
			return ErrInvalid
		}
	}
	if p.Links != nil {
		if len(*p.Links) > 16 {
			return ErrInvalid
		}
		for _, l := range *p.Links {
			u, e := url.Parse(l.URL)
			if e != nil || len(l.Name) > 128 || len(l.URL) > 2048 || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
				return ErrInvalid
			}
		}
	}
	return nil
}
func ValidatePublisher(p PublisherPatch, create bool) error {
	if p.PictureID != nil || p.BackgroundID != nil || p.PayoutWalletID != nil || p.DefaultPostTags != nil || p.DefaultPostCategories != nil {
		return ErrUnsupported
	}
	if !create && p.Name != nil {
		return ErrNameChange
	}
	if create && (p.Name == nil || p.Nick == nil) {
		return ErrInvalid
	}
	if p.Name != nil && (len(*p.Name) > 256 || !publisherName.MatchString(*p.Name) || authority.ValidID(*p.Name)) {
		return ErrInvalid
	}
	if p.Name != nil {
		switch strings.ToLower(*p.Name) {
		case "me", "individual", "organization", "invites", "of", "quota", "search", "leaderboard":
			return ErrInvalid
		}
	}
	for _, v := range []struct {
		p *string
		n int
	}{{p.Nick, 256}, {p.Bio, 4096}} {
		if v.p != nil && (!utf8.ValidString(*v.p) || utf8.RuneCountInString(*v.p) > v.n || strings.ContainsRune(*v.p, 0)) {
			return ErrInvalid
		}
	}
	if p.Nick != nil && strings.TrimSpace(*p.Nick) == "" {
		return ErrInvalid
	}
	return nil
}
