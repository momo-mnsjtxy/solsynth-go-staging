// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"mime"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/messager"
)

const VoiceMaxBytes = 8 << 20 // Owned safety bound, not an inferred upstream limit.

type PlaceholderRequest struct {
	Kind string `json:"kind"`
}
type PlaceholderUpdate struct {
	MessageID    string   `json:"message_id"`
	ContentChunk *string  `json:"content_chunk"`
	Progress     *float64 `json:"progress"`
}
type PlaceholderFinalize struct {
	MessageID     string    `json:"message_id"`
	Content       *string   `json:"content"`
	AttachmentsID *[]string `json:"attachments_id"`
}
type PlaceholderRepository interface {
	SendPlaceholder(context.Context, string, string, PlaceholderRequest) (messager.MessageProjection, error)
	UpdatePlaceholder(context.Context, string, PlaceholderUpdate) (messager.MessageProjection, error)
	FinalizePlaceholder(context.Context, string, PlaceholderFinalize) (messager.MessageProjection, error)
}

func ValidatePlaceholderKind(kind string) error {
	if kind != "streaming" && kind != "uploading" {
		return ErrInvalid
	}
	return nil
}
func ValidatePlaceholderUpdate(p PlaceholderUpdate) error {
	if !ValidID(p.MessageID) || p.ContentChunk != nil && !validText(*p.ContentChunk, 4096) || p.Progress != nil && (math.IsNaN(*p.Progress) || math.IsInf(*p.Progress, 0)) {
		return ErrInvalid
	}
	return nil
}
func ValidatePlaceholderFinalize(p PlaceholderFinalize) error {
	if !ValidID(p.MessageID) {
		return ErrInvalid
	}
	v := TextRequest{AttachmentsID: p.AttachmentsID}
	if p.Content != nil {
		v.Content = *p.Content
		v.ContentSet = true
	}
	_, e := ValidateText(v, true)
	return e
}

type VoiceRequest struct {
	Name               string
	MIMEType           string
	Data               []byte
	DurationMS         *int32
	ClientMessageID    *string
	Nonce              *string
	RepliedMessageID   *string
	ForwardedMessageID *string
}
type VoiceClip struct {
	ID, FileID, RoomID, SenderID, AccountID, Name, MIMEType string
	MessageID                                               *string
	Size                                                    int64
	DurationMS                                              *int32
	JoinedAt, CreatedAt, ExpiresAt                          time.Time
}
type VoiceRepository interface {
	SendVoice(context.Context, string, string, VoiceRequest) (messager.MessageProjection, error)
	OpenVoice(context.Context, string, string, string) (VoiceClip, io.ReadSeekCloser, error)
}

func ValidateVoice(p VoiceRequest) (VoiceRequest, error) {
	if len(p.Data) == 0 || len(p.Data) > VoiceMaxBytes || !validText(p.Name, 255) || strings.TrimSpace(p.Name) == "" || strings.ContainsAny(p.Name, "/\\") || p.Name == "." || p.Name == ".." || p.DurationMS != nil && (*p.DurationMS < 0 || *p.DurationMS > 24*60*60*1000) {
		return p, ErrInvalid
	}
	typ, params, e := mime.ParseMediaType(p.MIMEType)
	if e != nil || len(params) != 0 || len(typ) > 128 {
		return p, ErrInvalid
	}
	switch typ {
	case "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/mpeg", "audio/ogg", "audio/wav", "audio/x-wav", "audio/webm", "audio/aac", "audio/flac":
	default:
		return p, ErrInvalid
	}
	p.MIMEType = typ
	v, e := ValidateText(TextRequest{Content: "voice", ClientMessageID: p.ClientMessageID, Nonce: p.Nonce, RepliedMessageID: p.RepliedMessageID, ForwardedMessageID: p.ForwardedMessageID}, false)
	if e != nil {
		return p, e
	}
	p.ClientMessageID = v.ClientMessageID
	p.Nonce = nil
	return p, nil
}
func VoiceMeta(c VoiceClip) json.RawMessage {
	v := map[string]any{"voice_clip_id": c.ID, "voice_url": "/api/chat/" + c.RoomID + "/voice/" + c.ID, "mime_type": c.MIMEType, "size": c.Size}
	if c.DurationMS != nil {
		v["duration_ms"] = *c.DurationMS
	}
	if c.Name != "" {
		v["file_name"] = c.Name
	}
	b, _ := json.Marshal(v)
	return b
}
