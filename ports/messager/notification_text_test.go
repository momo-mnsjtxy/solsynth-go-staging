// SPDX-License-Identifier: AGPL-3.0-only
package messager

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNotificationTextPrecedence(t *testing.T) {
	tests := []struct {
		name string
		in   NotificationTextInput
		want string
	}{
		{"encrypted before deleted", NotificationTextInput{UserEncrypted: true, Deleted: true, Type: "voice", Mentioned: true, Change: NotificationEdited}, "[@] Encrypted message"},
		{"deleted before type", NotificationTextInput{Deleted: true, Type: "call.start", Change: NotificationEdited}, "Deleted a message"},
		{"ended ignores edit", NotificationTextInput{Type: "call.ended", Change: NotificationEdited}, "Call ended"},
		{"started ignores deletion event", NotificationTextInput{Type: "call.start", Change: NotificationDeleted}, "Call begun"},
		{"voice ignores deletion event", NotificationTextInput{Type: "voice", Change: NotificationDeleted}, "Voice message"},
		{"content edit mention", NotificationTextInput{Content: "hello", Change: NotificationEdited, Mentioned: true}, "[@] hello (edited)"},
		{"content deleted", NotificationTextInput{Content: "hello", Change: NotificationDeleted, Mentioned: true}, "[@] Deleted a message"},
		{"preview beats content", NotificationTextInput{StickerPreview: " sticker ", Content: "hello"}, " sticker "},
		{"blank metadata skipped", NotificationTextInput{StickerName: "\u0085\u2028\u2029\u00a0", StickerPreview: "\t\n", Content: " "}, " "},
		{"unknown type defaults", NotificationTextInput{Type: "future", Content: "hello"}, "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildNotificationText(tt.in, func(NotificationTranslation) (string, error) { t.Fatal("unexpected localization"); return "", nil })
			if err != nil {
				t.Fatal(err)
			}
			s, err := got.UTF8()
			if err != nil || s != tt.want {
				t.Fatalf("got %q, %v; want %q", s, err, tt.want)
			}
		})
	}
}

func TestNotificationLocalization(t *testing.T) {
	two := int32(2)
	zero := int32(0)
	tests := []struct {
		name   string
		in     NotificationTextInput
		key    string
		args   map[string]any
		suffix string
	}{
		{"streaming", NotificationTextInput{Type: "placeholder", Change: NotificationDeleted}, "chatPlaceholderStreaming", nil, ""},
		{"uploading", NotificationTextInput{Type: "placeholder", PlaceholderUploading: true}, "chatPlaceholderUploading", nil, ""},
		{"sticker default", NotificationTextInput{StickerName: "cat", StickerPreview: "ignored", Content: "ignored"}, "chatStickerOnlyNotificationBody", map[string]any{"name": "cat"}, ""},
		{"sticker plural", NotificationTextInput{StickerName: "cat", StickerCount: &two, Change: NotificationEdited}, "chatStickerOnlyNotificationBodyPlural", map[string]any{"name": "cat", "count": int32(2)}, " (edited)"},
		{"sticker zero is singular", NotificationTextInput{StickerName: "cat", StickerCount: &zero}, "chatStickerOnlyNotificationBody", map[string]any{"name": "cat"}, ""},
		{"one attachment", NotificationTextInput{AttachmentCount: 1}, "chatNotificationAttachmentOnlyBodySingular", map[string]any{"count": int32(1)}, ""},
		{"no attachments", NotificationTextInput{}, "chatNotificationAttachmentOnlyBodyPlural", map[string]any{"count": int32(0)}, ""},
		{"two attachments", NotificationTextInput{AttachmentCount: 2}, "chatNotificationAttachmentOnlyBodyPlural", map[string]any{"count": int32(2)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locale := "zh-Hans"
			tt.in.Locale = &locale
			calls := 0
			got, err := BuildNotificationText(tt.in, func(req NotificationTranslation) (string, error) {
				calls++
				if req.Key != tt.key || req.Locale == nil || *req.Locale != "zh-Hans" || !reflect.DeepEqual(req.Arguments, tt.args) {
					t.Fatalf("wrong localization request: %#v", req)
				}
				return strings.Repeat("文", 105), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s, err := got.UTF8()
			if err != nil || s != strings.Repeat("文", 105)+tt.suffix || calls != 1 {
				t.Fatalf("result %q err %v calls %d", s, err, calls)
			}
		})
	}
}

func TestNotificationUTF16Boundary(t *testing.T) {
	for _, preview := range []bool{false, true} {
		in := NotificationTextInput{Content: strings.Repeat("a", 98) + "😀tail", Change: NotificationEdited}
		if preview {
			in.StickerPreview = in.Content
			in.Content = "ignored"
		}
		got, err := BuildNotificationText(in, nil)
		if err != nil {
			t.Fatal(err)
		}
		s, err := got.UTF8()
		if err != nil || s != strings.Repeat("a", 98)+"😀 (edited)" {
			t.Fatalf("%q %v", s, err)
		}
	}
	got, err := BuildNotificationText(NotificationTextInput{Content: strings.Repeat("a", 99) + "😀", Mentioned: true, Change: NotificationEdited}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u := got.UTF16()
	if len(u) != 113 || u[103] != 0xd83d {
		t.Fatalf("unexpected UTF-16 boundary: %v", u)
	}
	if _, err = got.UTF8(); !errors.Is(err, ErrNotificationUnpairedSurrogate) {
		t.Fatalf("surrogate err %v", err)
	}
	u[0] = 0
	if got.UTF16()[0] != '[' {
		t.Fatal("mutable result escaped")
	}
	var empty NotificationText
	s, err := empty.UTF8()
	if err != nil || s != "" {
		t.Fatal("zero value")
	}
	// A low surrogate alone is rejected too (not emitted from valid UTF-8 inputs).
	if _, err = (NotificationText{units: []uint16{0xdc00}}).UTF8(); !errors.Is(err, ErrNotificationUnpairedSurrogate) {
		t.Fatal(err)
	}
}

func TestNotificationErrors(t *testing.T) {
	sentinel := errors.New("missing translation")
	_, err := BuildNotificationText(NotificationTextInput{Change: NotificationDeleted}, func(NotificationTranslation) (string, error) { return "", sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("deletion must not mask localization failure: %v", err)
	}
	for _, in := range []NotificationTextInput{
		{}, {AttachmentCount: -1}, {Change: 99}, {Content: "\xff"}, {StickerName: "\xff"}, {StickerPreview: "\xff"},
	} {
		if _, err := BuildNotificationText(in, nil); err == nil {
			t.Fatalf("expected error: %+v", in)
		}
	}
	if _, err := BuildNotificationText(NotificationTextInput{}, func(NotificationTranslation) (string, error) { return "\xff", nil }); err == nil {
		t.Fatal("accepted invalid localized UTF-8")
	}
}

func TestNotificationLocaleNullAndEmpty(t *testing.T) {
	empty := ""
	for _, locale := range []*string{nil, &empty} {
		_, err := BuildNotificationText(NotificationTextInput{Locale: locale}, func(req NotificationTranslation) (string, error) {
			if !reflect.DeepEqual(req.Locale, locale) {
				t.Fatalf("locale changed: %#v", req.Locale)
			}
			return "translated", nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
