// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"net/url"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	"unicode/utf8"
)

var publisherName = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9._-]*[a-zA-Z0-9])?$`)

func ValidateProfile(p ProfilePatch) error {
	for _, v := range []struct {
		p *string
		n int
	}{{p.Nick, 256}, {p.Language, 32}, {p.Region, 32}, {p.Bio, 4096}, {p.Pronouns, 128}, {p.Location, 256}, {p.TimeZone, 128}} {
		if v.p != nil && (!utf8.ValidString(*v.p) || utf8.RuneCountInString(*v.p) > v.n || strings.ContainsRune(*v.p, 0)) {
			return ErrInvalid
		}
	}
	if p.Name != nil {
		return ErrNameChange
	}
	if p.PictureID != nil || p.BackgroundID != nil {
		return ErrUnsupported
	}
	if p.Nick != nil && strings.TrimSpace(*p.Nick) == "" {
		return ErrInvalid
	}
	if p.TimeZone != nil && *p.TimeZone != "" {
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
