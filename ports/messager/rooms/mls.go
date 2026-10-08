// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
)

// Authored Solian maps, not imported shared schemas. This is an opaque delivery
// service: it does not validate MLS signatures, credentials, or group secrets.
const MLSEncryptionMode int32 = 3
const (
	MLSCommit      = 4
	MLSWelcome     = 5
	MLSApplication = 6
	MLSProposal    = 7
)
const MLSMaxEpoch int64 = 9007199254740991

type MLSRequest struct {
	Generation         *int64            `json:"-"`
	ResetCapable       bool              `json:"-"`
	ChatRoomID         string            `json:"chat_room_id,omitempty"`
	TargetGroupID      string            `json:"group_id,omitempty"`
	TargetAccountID    string            `json:"target_account_id,omitempty"`
	TargetDeviceID     string            `json:"target_device_id,omitempty"`
	KeyPackage         string            `json:"key_package,omitempty"`
	DeviceID           string            `json:"device_id,omitempty"`
	MLSDeviceID        string            `json:"mls_device_id,omitempty"`
	GroupID            string            `json:"mls_group_id,omitempty"`
	Policy             json.RawMessage   `json:"e2ee_policy,omitempty"`
	Epoch              *int64            `json:"epoch,omitempty"`
	StateVersion       *int64            `json:"state_version,omitempty"`
	NewEpoch           *int64            `json:"new_epoch,omitempty"`
	Meta               map[string]string `json:"meta,omitempty"`
	AccountIDs         []string          `json:"account_ids,omitempty"`
	GroupInfo          string            `json:"group_info,omitempty"`
	RatchetTree        string            `json:"ratchet_tree,omitempty"`
	Reason             string            `json:"reason,omitempty"`
	RecipientAccountID string            `json:"recipient_account_id,omitempty"`
	Payloads           []MLSPayload      `json:"payloads,omitempty"`
	Ciphertext         string            `json:"ciphertext,omitempty"`
	Header             string            `json:"header,omitempty"`
	ClientMessageID    string            `json:"client_message_id,omitempty"`
	RoomID             string            `json:"room_id,omitempty"`
	Payload            json.RawMessage   `json:"payload,omitempty"`
}
type MLSPayload struct {
	RecipientDeviceID string            `json:"recipient_device_id"`
	Ciphertext        string            `json:"ciphertext"`
	Header            string            `json:"header"`
	ClientMessageID   string            `json:"client_message_id"`
	Meta              map[string]string `json:"meta,omitempty"`
}
type MLSState struct {
	GroupID      string            `json:"group_id"`
	RoomID       string            `json:"room_id"`
	Epoch        int64             `json:"epoch"`
	StateVersion int64             `json:"state_version"`
	Meta         map[string]string `json:"meta"`
}
type EncryptionMeta struct {
	StateVersion *int64  `json:"state_version,omitempty"`
	Ciphertext   string  `json:"ciphertext"`
	Header       string  `json:"header"`
	Signature    *string `json:"signature"`
	Scheme       string  `json:"scheme"`
	Epoch        int64   `json:"epoch"`
}
type MLSEnvelope struct {
	ID           string `json:"id"`
	Type         int    `json:"type"`
	StateVersion int64  `json:"state_version"`
	Ciphertext   string `json:"ciphertext"`
	GroupID      string `json:"group_id"`
	Header       string `json:"header"`
	Epoch        int64  `json:"epoch"`
	Sequence     int64  `json:"sequence"`
}
type MLSRepository interface {
	MLS(context.Context, string, string, string, string, MLSRequest) (any, error)
}

var mlsID = regexp.MustCompile(`^[A-Za-z0-9_:.~-]{1,256}$`)

func ValidMLSIdentifier(s string) bool { return s != "." && s != ".." && mlsID.MatchString(s) }
func ValidMLSOpaque(s string, max int) bool {
	if len(s) == 0 || len(s) > base64.StdEncoding.EncodedLen(max) {
		return false
	}
	b, e := base64.StdEncoding.Strict().DecodeString(s)
	return e == nil && len(b) > 0 && len(b) <= max && base64.StdEncoding.EncodeToString(b) == s
}
func ValidateEncryption(p *EncryptionMeta) error {
	if p == nil || p.Scheme != "chat.mls.v2" || p.Epoch < 0 || p.Epoch > MLSMaxEpoch || !ValidMLSOpaque(p.Ciphertext, 65536) || !ValidMLSOpaque(p.Header, 4096) || p.Signature != nil && !ValidMLSOpaque(*p.Signature, 4096) {
		return ErrInvalid
	}
	return nil
}
func ValidMLSClientID(s string) bool {
	return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s && validText(s, 128)
}

// MLSWriteContext is an owned transport precondition. The account remains the
// authenticated principal; neither optional selector is a secret/device proof.
type MLSWriteContext struct {
	Encryption      *EncryptionMeta
	ClientMessageID *string
	Device          string
	Generation      *int64
}
type mlsWriteKey struct{}

func WithMLSWrite(ctx context.Context, p MLSWriteContext) context.Context {
	return context.WithValue(ctx, mlsWriteKey{}, p)
}
func MLSWriteFrom(ctx context.Context) MLSWriteContext {
	p, _ := ctx.Value(mlsWriteKey{}).(MLSWriteContext)
	return p
}

func ValidateMLSReferenceMeta(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrInvalid
	}
	for k, v := range fields {
		if string(v) == "null" {
			switch k {
			case "attachments_id", "replied_message_id", "forwarded_message_id", "survey_id", "fund_id":
				continue
			default:
				return ErrInvalid
			}
		}
		if k == "attachments_id" {
			var ids []string
			if json.Unmarshal(v, &ids) != nil || len(ids) > 32 {
				return ErrInvalid
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if !ValidID(id) || seen[id] {
					return ErrInvalid
				}
				seen[id] = true
			}
			continue
		}
		if k == "replied_message_id" {
			var id string
			if json.Unmarshal(v, &id) != nil || !ValidID(id) {
				return ErrInvalid
			}
			continue
		}
		if k == "forwarded_message_id" || k == "survey_id" || k == "fund_id" {
			return ErrUnsupported
		}
		return ErrInvalid
	}
	return nil
}

func (p *EncryptionMeta) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return ErrInvalid
	}
	if epoch, ok := fields["epoch"]; !ok || bytes.Equal(bytes.TrimSpace(epoch), []byte("null")) {
		return ErrInvalid
	}
	type plain EncryptionMeta
	var v plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(&v); e != nil {
		return e
	}
	*p = EncryptionMeta(v)
	return nil
}
