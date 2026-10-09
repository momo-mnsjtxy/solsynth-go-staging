// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

func mlsRoute(r *http.Request) (op, target, methodName string, fields []string) {
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(p) == 5 && p[0] == "api" && p[1] == "chat" && p[3] == "mls" && p[4] == "enable" {
		return "enable", p[2], "POST", []string{"mls_group_id", "e2ee_policy"}
	}
	if len(p) == 6 && p[0] == "api" && p[1] == "chat" && p[3] == "devices" && p[4] == "me" && p[5] == "joined" {
		return "joined", p[2], "POST", []string{"mls_device_id", "epoch"}
	}
	if len(p) < 3 || p[0] != "e2ee" || p[1] != "mls" {
		return
	}
	p = p[2:]
	switch {
	case strings.Join(p, "/") == "devices/me/kps":
		return "publish", "", "PUT", []string{"key_package", "device_id"}
	case strings.Join(p, "/") == "kp/status":
		return "status", "", "GET", nil
	case strings.Join(p, "/") == "users/ready/batch":
		return "ready", "", "POST", []string{"account_ids"}
	case strings.Join(p, "/") == "devices/me/reshare-required":
		return "recoveries", "", "GET", nil
	case strings.Join(p, "/") == "envelopes/pending":
		return "pending", "", "GET", nil
	case strings.Join(p, "/") == "messages/fanout":
		return "application", "", "POST", []string{"room_id", "payload"}
	case len(p) == 3 && p[0] == "keys" && p[2] == "devices":
		return "claim", p[1], "GET", nil
	case len(p) == 3 && p[0] == "devices" && p[2] == "revoke":
		return "revoke", p[1], "POST", nil
	case len(p) == 3 && p[0] == "envelopes" && p[2] == "ack":
		return "ack", p[1], "POST", nil
	case len(p) == 5 && p[0] == "devices" && p[1] == "me" && p[2] == "reshare-required" && p[4] == "complete":
		return "recovered", p[3], "POST", nil
	case len(p) >= 3 && p[0] == "groups":
		switch strings.Join(p[2:], "/") {
		case "bootstrap":
			return "bootstrap", p[1], "POST", []string{"epoch", "state_version", "meta"}
		case "commit":
			return "commit", p[1], "POST", []string{"epoch", "reason"}
		case "groupinfo":
			if r.Method == "GET" {
				return "groupinfo", p[1], "GET", nil
			}
			return "groupinfo-put", p[1], "PUT", []string{"group_info", "ratchet_tree", "epoch"}
		case "welcome/fanout":
			return "welcome", p[1], "POST", []string{"recipient_account_id", "payloads"}
		case "commit/fanout":
			return "commit-fanout", p[1], "POST", []string{"epoch", "ciphertext", "header", "client_message_id", "meta"}
		case "reshare-required":
			return "recover", p[1], "POST", []string{"chat_room_id", "group_id", "target_account_id", "target_device_id", "epoch", "reason"}
		case "reset":
			return "reset", p[1], "POST", []string{"new_epoch", "state_version", "reason"}
		}
	}
	return
}

// Recursively reject duplicate JSON names. No opaque header/ciphertext is parsed.
func uniqueMLSJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalid
	}
	token, e := d.Token()
	if e != nil {
		return ErrInvalid
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return ErrInvalid
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if e = uniqueMLSJSON(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e = uniqueMLSJSON(d, depth+1); e != nil {
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
func decodeMLS(w http.ResponseWriter, r *http.Request, out any, fields ...string) error {
	if r.Body == nil {
		return ErrInvalid
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if e != nil || !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if e = uniqueMLSJSON(d, 0); e != nil {
		return e
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil || top == nil {
		return ErrInvalid
	}
	allowed := map[string]bool{}
	for _, k := range fields {
		allowed[k] = true
	}
	for k := range top {
		if !allowed[k] {
			return ErrInvalid
		}
	}
	// The ordinary decoder checks Content-Type and top-level names. Its tighter
	// limit is unsuitable for opaque payloads, so retain its checks explicitly.
	if raw := r.Header.Get("Content-Type"); raw != "" {
		kind, _, e := mime.ParseMediaType(raw)
		if e != nil || kind != "application/json" {
			return ErrInvalid
		}
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrInvalid
	}
	return nil
}
func (s *Service) serveMLS(w http.ResponseWriter, r *http.Request) bool {
	op, target, want, fields := mlsRoute(r)
	if op == "" {
		return false
	}
	if r.Method != want {
		method(w, want)
		return true
	}
	repo, ok := s.Repository.(MLSRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return true
	}
	permission := ""
	if op == "enable" || op == "reset" {
		permission = "chat.update"
	}
	if op == "application" || op == "commit-fanout" || op == "welcome" {
		permission = "chat.messages.create"
	}
	principal, e := s.principal(r, permission)
	if e != nil {
		failure(w, e)
		return true
	}
	device := r.Header.Get("X-Device-Id")
	if len(r.Header.Values("X-Device-Id")) > 1 || op != "enable" && op != "joined" && !ValidMLSIdentifier(device) || device != "" && !ValidMLSIdentifier(device) {
		failure(w, ErrInvalid)
		return true
	}
	for k, v := range r.URL.Query() {
		if op != "ack" || k != "deviceId" || len(v) != 1 || v[0] != device {
			failure(w, ErrInvalid)
			return true
		}
	}
	if op == "ack" && r.URL.Query().Get("deviceId") != device {
		failure(w, ErrInvalid)
		return true
	}
	var req MLSRequest
	if len(fields) > 0 {
		e = decodeMLS(w, r, &req, fields...)
	} else {
		e = empty(w, r)
	}
	if e != nil {
		failure(w, e)
		return true
	}
	if len(r.Header.Values("X-MLS-State-Version")) > 1 || len(r.Header.Values("X-MLS-Reset-Capability")) > 1 {
		failure(w, ErrInvalid)
		return true
	}
	if version := r.Header.Get("X-MLS-State-Version"); version != "" {
		n, x := strconv.ParseInt(version, 10, 64)
		if x != nil || n < 1 || n > MLSMaxEpoch {
			failure(w, ErrInvalid)
			return true
		}
		req.Generation = &n
	}
	req.ResetCapable = r.Header.Get("X-MLS-Reset-Capability") == "generation-v1"
	if op == "joined" {
		if device != "" && device != req.MLSDeviceID {
			failure(w, ErrInvalid)
			return true
		}
		device = req.MLSDeviceID
	}
	ctx := r.Context()
	if s.Bind != nil {
		ctx = s.Bind(ctx, principal)
	}
	out, e := repo.MLS(ctx, principal.Account.ID, device, op, target, req)
	if e != nil {
		failure(w, e)
	} else if out == nil {
		send(w, 204, nil)
	} else {
		send(w, 200, out)
	}
	return true
}

func (s *Service) textFields() []string {
	fields := []string{"content", "client_message_id", "nonce", "replied_message_id", "attachments_id", "meta", "embeds", "thread_id", "forwarded_message_id", "is_thread_root", "survey_id", "fund_id", "meet_id", "notable_day_id", "calendar_event_id", "location_name", "location_address", "location_wkt"}
	if enabled, ok := s.Repository.(interface{ MLSDeliveryEnabled() bool }); ok && enabled.MLSDeliveryEnabled() {
		fields = append(fields, "type", "encryption_meta")
	}
	return fields
}
func mlsWriteContext(r *http.Request) (MLSWriteContext, error) {
	p := MLSWriteContext{Device: r.Header.Get("X-Device-Id")}
	if len(r.Header.Values("X-Device-Id")) > 1 || len(r.Header.Values("X-MLS-State-Version")) > 1 || p.Device != "" && !ValidMLSIdentifier(p.Device) {
		return p, ErrInvalid
	}
	if raw := r.Header.Get("X-MLS-State-Version"); raw != "" {
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n < 1 || n > MLSMaxEpoch {
			return p, ErrInvalid
		}
		p.Generation = &n
	}
	return p, nil
}

func (s *Service) decodeText(w http.ResponseWriter, r *http.Request, out *TextRequest) error {
	if enabled, ok := s.Repository.(interface{ MLSDeliveryEnabled() bool }); ok && enabled.MLSDeliveryEnabled() {
		return decodeMLS(w, r, out, s.textFields()...)
	}
	return decode(w, r, out, s.textFields()...)
}

func (s *Service) decodeMLSDelete(w http.ResponseWriter, r *http.Request) (TextRequest, error) {
	var out TextRequest
	if enabled, ok := s.Repository.(interface{ MLSDeliveryEnabled() bool }); !ok || !enabled.MLSDeliveryEnabled() {
		return out, empty(w, r)
	}
	if r.Body == nil {
		return out, nil
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if e != nil {
		return out, ErrInvalid
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	if e = decodeMLS(w, r, &out, "encryption_meta", "client_message_id", "nonce"); e != nil {
		return out, e
	}
	return ValidateText(out, true)
}
