// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

func accountContext(r Repository, ctx context.Context, p authority.Principal) context.Context {
	if b, ok := r.(interface {
		BindAuthority(context.Context, authority.Principal) context.Context
	}); ok {
		return b.BindAuthority(ctx, p)
	}
	return ctx
}
func uniqueProfileJSON(d *json.Decoder, depth int) error {
	if depth > 24 {
		return ErrInvalid
	}
	token, e := d.Token()
	if e != nil {
		return e
	}
	v, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch v {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := token.(string)
			if !ok || seen[strings.ToLower(key)] {
				return ErrInvalid
			}
			seen[strings.ToLower(key)] = true
			if e = uniqueProfileJSON(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e = uniqueProfileJSON(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
