// SPDX-License-Identifier: AGPL-3.0-only
package autocomplete

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid         = &Failure{"AUTOCOMPLETE_INVALID", "Invalid autocomplete request.", 400}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrForbidden       = &Failure{"AUTOCOMPLETE_FORBIDDEN", "Current room membership and access are required.", 403}
	ErrUnavailable     = &Failure{"AUTOCOMPLETE_UNAVAILABLE", "Autocomplete dependencies are unavailable.", 503}
)

type Suggestion struct {
	Type    string `json:"type"`
	Keyword string `json:"keyword"`
	Data    any    `json:"data"`
}
type Query struct{ Kind, Prefix, Text string }

func Parse(content string) (Query, error) {
	if !utf8.ValidString(content) || len(content) > 2048 || utf8.RuneCountInString(content) > 512 {
		return Query{}, ErrInvalid
	}
	if strings.TrimSpace(content) == "" {
		return Query{}, nil
	}
	if strings.HasPrefix(content, ":") {
		if len(content)-1 > 257 {
			return Query{}, ErrInvalid
		}
		return Query{Kind: "sticker", Prefix: ":", Text: content[1:]}, nil
	}
	if !strings.HasPrefix(content, "@") {
		return Query{}, nil
	}
	v := content[1:]
	kind, prefix, text := "user", "@", v
	if strings.Contains(v, "/") {
		parts := strings.SplitN(v, "/", 2)
		text = parts[1]
		switch parts[0] {
		case "u":
			kind, prefix = "user", "@u/"
		case "p":
			kind, prefix = "publisher", "@p/"
		case "r":
			kind, prefix = "realm", "@r/"
		default:
			return Query{}, nil
		}
	}
	if len(text) > 256 {
		return Query{}, ErrInvalid
	}
	return Query{kind, prefix, text}, nil
}

type Repository interface {
	Suggest(context.Context, string, string, string) ([]Suggestion, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}
type RoomPolicy interface {
	CanReadInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
}
