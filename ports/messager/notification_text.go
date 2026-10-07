// SPDX-License-Identifier: AGPL-3.0-only
// Adapted from Solsynth/DysonNetwork ChatService.cs, revision
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-07.
package messager

import (
	"errors"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// NotificationChange is a domain event classification, not a wire packet type.
type NotificationChange uint8

const (
	NotificationCreated NotificationChange = iota
	NotificationEdited
	NotificationDeleted
)

// NotificationTextInput contains resolved domain values, not shared/wire models.
// UserEncrypted and PlaceholderUploading must be established by the caller from
// licensed, verified model semantics. This package does not resolve recipients.
// StickerName/Preview mean string-valued metadata; Count is already converted
// to an int32 (upstream Convert.ToInt32 can fail and must not be ignored).
type NotificationTextInput struct {
	UserEncrypted, Deleted, PlaceholderUploading, Mentioned bool
	Type, Content, StickerName, StickerPreview              string
	StickerCount                                            *int32
	AttachmentCount                                         int32
	Change                                                  NotificationChange
	Locale                                                  *string
}

// NotificationTranslation contains the exact localization key and arguments.
// Locale preserves null versus empty for the real localization adapter.
type NotificationTranslation struct {
	Key       string
	Locale    *string
	Arguments map[string]any
}
type NotificationLocalize func(NotificationTranslation) (string, error)

// NotificationText retains .NET UTF-16 truncation, including a possible lone
// surrogate at the 100-unit boundary. UTF8 refuses that case instead of silently
// substituting a replacement character. A transport must choose its own policy.
type NotificationText struct{ units []uint16 }

func (t NotificationText) UTF16() []uint16 { return append([]uint16{}, t.units...) }

var ErrNotificationUnpairedSurrogate = errors.New("notification text contains an unpaired UTF-16 surrogate")

func (t NotificationText) UTF8() (string, error) {
	for i := 0; i < len(t.units); i++ {
		u := t.units[i]
		if u >= 0xd800 && u <= 0xdbff {
			if i+1 >= len(t.units) || t.units[i+1] < 0xdc00 || t.units[i+1] > 0xdfff {
				return "", ErrNotificationUnpairedSurrogate
			}
			i++
		} else if u >= 0xdc00 && u <= 0xdfff {
			return "", ErrNotificationUnpairedSurrogate
		}
	}
	return string(utf16.Decode(t.units)), nil
}

// BuildNotificationText ports BuildNotificationBody's branch order and suffixes.
// It performs no delivery, identity lookup, persistence, or authorization.
func BuildNotificationText(in NotificationTextInput, localize NotificationLocalize) (NotificationText, error) {
	if in.AttachmentCount < 0 || in.Change > NotificationDeleted {
		return NotificationText{}, errors.New("invalid notification domain input")
	}
	units := func(s string) ([]uint16, error) {
		if !utf8.ValidString(s) {
			return nil, errors.New("notification input is not valid UTF-8")
		}
		return utf16.Encode([]rune(s)), nil
	}
	translate := func(key string, args map[string]any) ([]uint16, error) {
		if localize == nil {
			return nil, errors.New("notification localization is required")
		}
		s, err := localize(NotificationTranslation{Key: key, Locale: in.Locale, Arguments: args})
		if err != nil {
			return nil, err
		}
		return units(s)
	}
	// .NET IsNullOrWhiteSpace uses Unicode separators plus TAB..CR and NEL;
	// unlike Go's unicode.IsSpace, this intentionally excludes no extra characters.
	nonblank := func(s string) bool {
		for _, r := range s {
			if !(r >= '\t' && r <= '\r') && r != 0x85 && !unicode.Is(unicode.Zs, r) && !unicode.Is(unicode.Zl, r) && !unicode.Is(unicode.Zp, r) {
				return true
			}
		}
		return false
	}
	var body []uint16
	var err error
	switch {
	case in.UserEncrypted:
		body, err = units("Encrypted message")
	case in.Deleted:
		body, err = units("Deleted a message")
	default:
		switch in.Type {
		case "call.ended":
			body, err = units("Call ended")
		case "call.start":
			body, err = units("Call begun")
		case "voice":
			body, err = units("Voice message")
		case "placeholder":
			key := "chatPlaceholderStreaming"
			if in.PlaceholderUploading {
				key = "chatPlaceholderUploading"
			}
			body, err = translate(key, nil)
		default:
			switch {
			case nonblank(in.StickerName):
				if !utf8.ValidString(in.StickerName) {
					return NotificationText{}, errors.New("sticker name is not valid UTF-8")
				}
				count := int32(1)
				if in.StickerCount != nil {
					count = *in.StickerCount
				}
				key := "chatStickerOnlyNotificationBody"
				args := map[string]any{"name": in.StickerName}
				if count > 1 {
					key = "chatStickerOnlyNotificationBodyPlural"
					args["count"] = count
				}
				body, err = translate(key, args)
			case nonblank(in.StickerPreview):
				body, err = units(in.StickerPreview)
				if len(body) > 100 {
					body = body[:100]
				}
			case in.Content != "":
				body, err = units(in.Content)
				if len(body) > 100 {
					body = body[:100]
				}
			default:
				key := "chatNotificationAttachmentOnlyBodyPlural"
				if in.AttachmentCount == 1 {
					key = "chatNotificationAttachmentOnlyBodySingular"
				}
				body, err = translate(key, map[string]any{"count": in.AttachmentCount})
			}
			if err == nil {
				switch in.Change {
				case NotificationEdited:
					suffix, _ := units(" (edited)")
					body = append(body, suffix...)
				case NotificationDeleted:
					body, _ = units("Deleted a message")
				}
			}
		}
	}
	if err != nil {
		return NotificationText{}, err
	}
	if in.Mentioned {
		prefix, _ := units("[@] ")
		body = append(prefix, body...)
	}
	return NotificationText{units: body}, nil
}
