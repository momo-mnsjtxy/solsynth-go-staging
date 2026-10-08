// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
)

var hlsDuration = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

// ProjectHLS accepts a bounded MPEG-TS media-playlist subset of RFC 8216.
// Every segment must be a single basename in the same owned output. It rejects
// encryption/key URLs, master playlists, byte ranges, maps and unknown tags.
// No filesystem path or external URI is copied into an exposed reference.
func ProjectHLS(data []byte, stream, output string, maxSegments int) ([]byte, map[string]bool, error) {
	if !authority.ValidID(stream) || !authority.ValidID(output) || maxSegments < 1 || maxSegments > 2000 || len(data) < 8 || len(data) > 256<<10 || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return nil, nil, ErrPlaylist
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > maxSegments*4+32 || lines[0] != "#EXTM3U" {
		return nil, nil, ErrPlaylist
	}
	seen := map[string]bool{}
	assets := map[string]bool{}
	out := []string{"#EXTM3U"}
	target := 0
	pending := false
	ended := false
	count := 0
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		if ended || len(line) > 1024 || strings.ContainsAny(line, "\r\t") || strings.TrimSpace(line) != line {
			return nil, nil, ErrPlaylist
		}
		for _, r := range line {
			if r < 32 || r == 127 {
				return nil, nil, ErrPlaylist
			}
		}
		if line[0] != '#' {
			if !pending || !ValidOutputAsset(line) || !strings.HasSuffix(line, ".ts") || assets[line] {
				return nil, nil, ErrPlaylist
			}
			pending = false
			count++
			if count > maxSegments {
				return nil, nil, ErrPlaylist
			}
			assets[line] = true
			out = append(out, "/api/livestreams/"+stream+"/hls/"+output+"/"+line)
			continue
		}
		key, value, hasValue := strings.Cut(line, ":")
		switch key {
		case "#EXT-X-VERSION", "#EXT-X-TARGETDURATION", "#EXT-X-MEDIA-SEQUENCE", "#EXT-X-DISCONTINUITY-SEQUENCE", "#EXT-X-PLAYLIST-TYPE", "#EXT-X-ALLOW-CACHE":
			if pending || seen[key] || !hasValue {
				return nil, nil, ErrPlaylist
			}
			seen[key] = true
			switch key {
			case "#EXT-X-VERSION":
				n, e := strconv.Atoi(value)
				if e != nil || n < 1 || n > 7 {
					return nil, nil, ErrPlaylist
				}
			case "#EXT-X-TARGETDURATION":
				n, e := strconv.Atoi(value)
				if e != nil || n < 1 || n > 60 {
					return nil, nil, ErrPlaylist
				}
				target = n
			case "#EXT-X-MEDIA-SEQUENCE", "#EXT-X-DISCONTINUITY-SEQUENCE":
				if _, e := strconv.ParseUint(value, 10, 63); e != nil {
					return nil, nil, ErrPlaylist
				}
			case "#EXT-X-PLAYLIST-TYPE":
				if value != "EVENT" && value != "VOD" {
					return nil, nil, ErrPlaylist
				}
			case "#EXT-X-ALLOW-CACHE":
				if value != "YES" && value != "NO" {
					return nil, nil, ErrPlaylist
				}
			}
		case "#EXTINF":
			if pending || !hasValue || target == 0 {
				return nil, nil, ErrPlaylist
			}
			duration, _, comma := strings.Cut(value, ",")
			n, e := strconv.ParseFloat(duration, 64)
			if !comma || !hlsDuration.MatchString(duration) || e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || math.Round(n) > float64(target) {
				return nil, nil, ErrPlaylist
			}
			pending = true
		case "#EXT-X-PROGRAM-DATE-TIME":
			if !hasValue {
				return nil, nil, ErrPlaylist
			}
			if _, e := time.Parse(time.RFC3339Nano, value); e != nil {
				return nil, nil, ErrPlaylist
			}
		case "#EXT-X-DISCONTINUITY", "#EXT-X-INDEPENDENT-SEGMENTS":
			if hasValue || key == "#EXT-X-INDEPENDENT-SEGMENTS" && seen[key] {
				return nil, nil, ErrPlaylist
			}
			if key == "#EXT-X-INDEPENDENT-SEGMENTS" {
				seen[key] = true
			}
		case "#EXT-X-ENDLIST":
			if pending || hasValue {
				return nil, nil, ErrPlaylist
			}
			ended = true
		default:
			return nil, nil, ErrPlaylist
		}
		out = append(out, line)
	}
	if pending || target == 0 || count == 0 {
		return nil, nil, ErrPlaylist
	}
	return []byte(strings.Join(out, "\n") + "\n"), assets, nil
}
