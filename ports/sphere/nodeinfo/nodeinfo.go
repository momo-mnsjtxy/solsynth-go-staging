// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed translation of licensed DysonNetwork NodeInfoController.cs
// at 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.

// Package nodeinfo implements the bounded Sphere NodeInfo response candidate.
// It does not establish upstream middleware or Shared global-filter parity.
package nodeinfo

import "context"

// Stats preserves the controller's signed 32-bit CountAsync result domain.
// ActivePublishers means non-Fediverse publishers with a non-null URI, not
// publishers active within either time period. The source has no time predicate.
type Stats struct {
	TotalPublishers  int32
	ActivePublishers int32
	TotalPosts       int32
}

type Reader interface {
	ReadStats(context.Context) (Stats, error)
}

type Link struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

type Discovery struct {
	Links []Link `json:"links"`
}

// Discover preserves both source link order and the surprising omission of
// /.well-known from the hrefs. A nil domain corresponds to missing configuration;
// an explicitly empty domain stays empty, just like C#'s null-coalescing fallback.
func Discover(domain *string) Discovery {
	value := "localhost"
	if domain != nil {
		value = *domain
	}
	return Discovery{Links: []Link{
		{Rel: "http://nodeinfo.diaspora.software/ns/schema/2.1", Href: "https://" + value + "/nodeinfo/2.1"},
		{Rel: "http://nodeinfo.diaspora.software/ns/schema/2.0", Href: "https://" + value + "/nodeinfo/2.0"},
	}}
}

type Software struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Services struct {
	Inbound  []string `json:"inbound"`
	Outbound []string `json:"outbound"`
}

type Users struct {
	Total          int32 `json:"total"`
	ActiveMonth    int32 `json:"active_month"`
	ActiveHalfyear int32 `json:"active_halfyear"`
}

type Usage struct {
	Users         Users `json:"users"`
	LocalPosts    int32 `json:"local_posts"`
	LocalComments int32 `json:"local_comments"`
}

type Accounts struct {
	MaxFeaturedObjects int32 `json:"max_featured_objects"`
}

type Statuses struct {
	MaxCharacters       int32 `json:"max_characters"`
	MaxMediaAttachments int32 `json:"max_media_attachments"`
}

type MediaAttachments struct {
	SupportedMIMETypes  []string `json:"supported_mime_types"`
	ImageSizeLimit      int32    `json:"image_size_limit"`
	ImageMatrixLimit    int32    `json:"image_matrix_limit"`
	VideoSizeLimit      int32    `json:"video_size_limit"`
	VideoFrameRateLimit int32    `json:"video_frame_rate_limit"`
	VideoMatrixLimit    int32    `json:"video_matrix_limit"`
}

type Configuration struct {
	URLs             *struct{}        `json:"urls,omitempty"`
	Accounts         Accounts         `json:"accounts"`
	Statuses         Statuses         `json:"statuses"`
	MediaAttachments MediaAttachments `json:"media_attachments"`
}

// Document has explicit snake_case tags matching the licensed Sphere startup,
// even where they differ from conventional NodeInfo camelCase spellings.
type Document struct {
	Version           string        `json:"version,omitempty"`
	Software          Software      `json:"software"`
	Protocols         []string      `json:"protocols"`
	Services          Services      `json:"services"`
	Usage             Usage         `json:"usage"`
	OpenRegistrations bool          `json:"open_registrations"`
	Configuration     Configuration `json:"configuration"`
	Metadata          *struct{}     `json:"metadata,omitempty"`
}

func document(stats Stats) Document {
	return Document{
		Software:          Software{Name: "dysonnetwork", Version: "1.0.0"},
		Protocols:         []string{"activitypub"},
		Services:          Services{Inbound: []string{}, Outbound: []string{"activitypub"}},
		Usage:             Usage{Users: Users{Total: stats.TotalPublishers, ActiveMonth: stats.ActivePublishers, ActiveHalfyear: stats.ActivePublishers}, LocalPosts: stats.TotalPosts},
		OpenRegistrations: true,
		Configuration: Configuration{
			Accounts:         Accounts{MaxFeaturedObjects: 10},
			Statuses:         Statuses{MaxCharacters: 5000, MaxMediaAttachments: 4},
			MediaAttachments: MediaAttachments{SupportedMIMETypes: []string{"image/*", "video/*", "audio/*"}, ImageSizeLimit: 10_000_000, ImageMatrixLimit: 4_000_000, VideoSizeLimit: 40_000_000, VideoFrameRateLimit: 30, VideoMatrixLimit: 2_000_000},
		},
	}
}

// Version20 intentionally has neither a version member nor metadata.
func Version20(stats Stats) Document {
	doc := document(stats)
	doc.Configuration.URLs = &struct{}{}
	return doc
}

// Version21 intentionally omits configuration.urls.
func Version21(stats Stats) Document {
	doc := document(stats)
	doc.Version = "2.1"
	doc.Metadata = &struct{}{}
	return doc
}
