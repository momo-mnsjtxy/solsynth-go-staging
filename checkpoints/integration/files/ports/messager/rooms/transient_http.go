// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

// Canonical storage refusals retain their actionable status. Unknown storage
// errors still use the room service's closed, non-disclosing fallback.
func voiceFailure(w http.ResponseWriter, err error) {
	var f *media.Failure
	if errors.As(err, &f) {
		send(w, f.Status, f)
		return
	}
	failure(w, err)
}

func (s *Service) serveTransient(w http.ResponseWriter, r *http.Request, p []string) bool {
	voiceRead := len(p) == 3 && p[1] == "voice"
	create := len(p) == 3 && p[1] == "messages" && (p[2] == "voice" || p[2] == "placeholder")
	if !voiceRead && !create {
		return false
	}
	if !ValidID(p[0]) || voiceRead && !ValidID(p[2]) || r.URL.RawQuery != "" {
		failure(w, ErrInvalid)
		return true
	}
	method := "POST"
	if voiceRead {
		method = "GET"
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		send(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
		return true
	}
	if s == nil || s.Repository == nil || s.Authority == nil {
		failure(w, ErrUnavailable)
		return true
	}
	var principal authority.Principal
	var e error
	if voiceRead {
		principal, e = s.Authority.AuthenticateRequest(r)
		if e != nil {
			present := len(r.Header.Values("Authorization")) != 0
			if marker, ok := s.Authority.(interface{ RequestIdentityPresent(*http.Request) bool }); ok {
				present = present || marker.RequestIdentityPresent(r)
			}
			if !errors.Is(e, authority.ErrInvalid) || present {
				failure(w, e)
				return true
			}
			principal = authority.Principal{}
		} else {
			principal, e = s.Authority.AuthenticatePrincipal(r.Context(), principal)
			if e != nil {
				failure(w, e)
				return true
			}
		}
	} else {
		principal, e = s.principal(r, "chat.messages.create")
		if e != nil {
			failure(w, e)
			return true
		}
	}
	actor := principal.Account.ID
	if actor != "" && !ValidID(actor) || !voiceRead && actor == "" {
		failure(w, authority.ErrInvalid)
		return true
	}
	ctx := r.Context()
	if s.Bind != nil && actor != "" {
		ctx = s.Bind(ctx, principal)
	}
	if voiceRead {
		repo, ok := s.Repository.(VoiceRepository)
		if !ok {
			failure(w, ErrUnavailable)
			return true
		}
		clip, body, e := repo.OpenVoice(ctx, actor, p[0], p[2])
		if e != nil {
			voiceFailure(w, e)
			return true
		}
		if body == nil {
			failure(w, ErrUnavailable)
			return true
		}
		defer body.Close()
		w.Header().Set("Content-Type", clip.MIMEType)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, "", time.Time{}, body)
		return true
	}
	if p[2] == "placeholder" {
		repo, ok := s.Repository.(PlaceholderRepository)
		if !ok {
			failure(w, ErrUnavailable)
			return true
		}
		var input PlaceholderRequest
		if e = decode(w, r, &input, "kind"); e != nil {
			failure(w, e)
			return true
		}
		if e = ValidatePlaceholderKind(input.Kind); e != nil {
			failure(w, e)
			return true
		}
		v, e := repo.SendPlaceholder(ctx, actor, p[0], input)
		if e != nil {
			failure(w, e)
		} else {
			send(w, 200, v)
		}
		return true
	}
	repo, ok := s.Repository.(VoiceRepository)
	if !ok {
		failure(w, ErrUnavailable)
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, VoiceMaxBytes+65536)
	input, e := readVoiceMultipart(r)
	if e != nil {
		failure(w, e)
		return true
	}
	v, e := repo.SendVoice(ctx, actor, p[0], input)
	if e != nil {
		voiceFailure(w, e)
	} else {
		send(w, 200, v)
	}
	return true
}
func readVoiceMultipart(r *http.Request) (VoiceRequest, error) {
	var p VoiceRequest
	reader, e := r.MultipartReader()
	if e != nil {
		return p, ErrInvalid
	}
	seen := map[string]bool{}
	aliases := map[string]string{"file": "file", "File": "file", "nonce": "nonce", "Nonce": "nonce", "client_message_id": "client", "clientMessageId": "client", "ClientMessageId": "client", "durationMs": "duration", "DurationMs": "duration", "duration_ms": "duration", "repliedMessageId": "reply", "RepliedMessageId": "reply", "replied_message_id": "reply", "forwardedMessageId": "forward", "ForwardedMessageId": "forward", "forwarded_message_id": "forward"}
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return p, ErrInvalid
		}
		name, ok := aliases[part.FormName()]
		if !ok || seen[name] {
			part.Close()
			return p, ErrInvalid
		}
		seen[name] = true
		if name == "file" {
			if part.FileName() == "" {
				part.Close()
				return p, ErrInvalid
			}
			p.Name = part.FileName()
			p.MIMEType = part.Header.Get("Content-Type")
			p.Data, e = io.ReadAll(io.LimitReader(part, VoiceMaxBytes+1))
			part.Close()
			if e != nil || len(p.Data) > VoiceMaxBytes {
				return p, ErrInvalid
			}
			continue
		}
		if part.FileName() != "" {
			part.Close()
			return p, ErrInvalid
		}
		b, e := io.ReadAll(io.LimitReader(part, 4097))
		part.Close()
		if e != nil || len(b) > 4096 {
			return p, ErrInvalid
		}
		v := string(b)
		switch name {
		case "nonce":
			p.Nonce = &v
		case "client":
			p.ClientMessageID = &v
		case "duration":
			n, e := strconv.ParseInt(v, 10, 32)
			if e != nil {
				return p, ErrInvalid
			}
			d := int32(n)
			p.DurationMS = &d
		case "reply":
			p.RepliedMessageID = &v
		case "forward":
			p.ForwardedMessageID = &v
		}
	}
	if !seen["file"] || strings.TrimSpace(p.MIMEType) == "" {
		return p, ErrInvalid
	}
	return ValidateVoice(p)
}
