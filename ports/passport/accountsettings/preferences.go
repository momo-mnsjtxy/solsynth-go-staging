// SPDX-License-Identifier: AGPL-3.0-only
// String preference levels and HTTP paths are explicitly owned contracts.
package accountsettings

import (
	"context"
	"errors"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	"unicode/utf8"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"SETTINGS_INVALID", "Invalid account setting.", 400}
	ErrUnavailable = &Failure{"SETTINGS_UNAVAILABLE", "Account settings are unavailable.", 503}
	ErrLimit       = &Failure{"SETTINGS_LIMIT", "Too many preference overrides.", 409}
)

func Normalize(e error) error {
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) || errors.As(e, &a) {
		return e
	}
	return ErrUnavailable
}

type Preference struct {
	Topic       string    `json:"topic"`
	Preference  string    `json:"preference"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type PreferenceInput struct {
	Preference  string `json:"preference"`
	Description string `json:"description"`
}

var topicPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func ValidTopic(v string) bool { return topicPattern.MatchString(v) }
func ValidPreference(v PreferenceInput) bool {
	return (v.Preference == "normal" || v.Preference == "silent" || v.Preference == "reject") && utf8.ValidString(v.Description) && !strings.ContainsRune(v.Description, 0) && len(v.Description) <= 1024
}

type Repository interface {
	List(context.Context, string, string) ([]Preference, error)
	Set(context.Context, string, string, string, PreferenceInput) (Preference, error)
	Reset(context.Context, string, string, string) error
}
