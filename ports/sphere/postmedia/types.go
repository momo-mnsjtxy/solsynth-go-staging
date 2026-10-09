// SPDX-License-Identifier: AGPL-3.0-only
// Package postmedia describes the bounded attachment portion of the licensed
// composer contract. Storage authority remains in the canonical media provider.
package postmedia

import (
	"strings"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

const MaxAttachments = 32

// Attachment is the nullable SnCloudFile projection used by direct uploads.
// Absent processors and remote storage are represented honestly, not fabricated.
type Attachment map[string]any

func Project(f media.File) Attachment {
	return Attachment(media.LegacyFile(f))
}

// NormalizeIDs preserves composer order and rejects duplicate identities,
// external URLs, extension suffixes, and IDs outside the canonical media store.
func NormalizeIDs(ids []string) ([]string, bool) {
	if len(ids) > MaxAttachments {
		return nil, false
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.ToLower(id)
		if !authority.ValidID(id) {
			return nil, false
		}
		if seen[id] {
			return nil, false
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, true
}
