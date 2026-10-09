// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"io"
	"regexp"
	"strings"
	"time"
)

const EgressContract = "livekit-start-egress-json-v1"
const ArchiveImportContract = "livestream-hls-mp4-import-v1"

var ErrOutputUnavailable = &Failure{"LIVE_STREAM_OUTPUT_UNAVAILABLE", "The selected output capability is unavailable.", 503}
var ErrOutputUncertain = &Failure{"LIVE_STREAM_OUTPUT_UNCERTAIN", "The provider outcome is not yet confirmed.", 409}
var ErrPlaylist = &Failure{"LIVE_STREAM_PLAYLIST_UNSUPPORTED", "The playlist is not a supported bounded HLS projection.", 503}
var outputIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var assetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func ValidOutputDestination(id string) bool { return outputIDPattern.MatchString(id) }
func ValidOutputAsset(name string) bool {
	return assetPattern.MatchString(name) && !strings.Contains(name, "..") && (strings.HasSuffix(name, ".m3u8") || strings.HasSuffix(name, ".ts"))
}

type EgressRequest struct {
	Key            string   `json:"-"`
	DestinationIDs []string `json:"destination_ids"`
	RtmpURLs       []string `json:"rtmp_urls"`
	FilePath       *string  `json:"file_path"`
}
type HLSRequest struct {
	Key             string  `json:"-"`
	DestinationID   string  `json:"destination_id"`
	PlaylistName    *string `json:"playlist_name"`
	SegmentDuration *int    `json:"segment_duration"`
	SegmentCount    *int    `json:"segment_count"`
	Layout          *string `json:"layout"`
	HLSBaseURL      *string `json:"hls_base_url"`
}
type OutputResponse struct {
	EgressID     string  `json:"egress_id"`
	OutputID     string  `json:"output_id"`
	State        string  `json:"state"`
	Reason       string  `json:"reason,omitempty"`
	PlaylistURL  *string `json:"playlist_url,omitempty"`
	PlaylistName *string `json:"playlist_name,omitempty"`
}
type OutputRepository interface {
	GetOutput(context.Context, string, string, string) (OutputResponse, error)
	StartEgress(context.Context, string, string, EgressRequest) (OutputResponse, error)
	StartHLS(context.Context, string, string, HLSRequest) (OutputResponse, error)
	StopOutput(context.Context, string, string, string) error
	OpenHLS(context.Context, string, string, string, string) (Playback, error)
	RequestArchiveImport(context.Context, string, string, string, string) (ArchiveImport, error)
	GetArchiveImport(context.Context, string, string, string, string) (ArchiveImport, error)
}
type Playback struct {
	Body       []byte
	MIME       string
	ModifiedAt time.Time
}

// OutputProvider handles only explicitly configured destinations. The body is
// constructed by its private operator policy; callers pass destination IDs.
type OutputProvider interface {
	OutputContract() string
	OutputPolicyFingerprint([]string, string) ([]byte, error)
	OutputUsesRelay([]string, string) (bool, error)
	StartOutput(context.Context, OutputStart) (ProviderOutput, error)
	ListOutputs(context.Context, string) ([]ProviderOutput, error)
	StopOutput(context.Context, string, string) (ProviderOutput, error)
}
type OutputStart struct {
	ID, Room, Kind  string
	Destinations    []string
	Playlist        string
	SegmentDuration int
	Layout          string
}
type ProviderOutput struct {
	ID, Room string
	Status   int
	Playlist string
	Absent   bool
}

func (p ProviderOutput) Terminal() bool { return p.Absent || p.Status >= 3 && p.Status <= 6 }

// ArtifactStore is a read-only capability of the configured canonical object
// adapter, never a general file or URL opener. namespace is an operator ID;
// output is an owned UUID; asset is a validated single basename.
type ArtifactStore interface {
	StreamingReady(context.Context, string) error
	OpenStreamingArtifact(context.Context, string, string, string, int64) (io.ReadSeekCloser, int64, time.Time, error)
}

// ArchiveImporter is an explicitly configured, idempotent host capability. It
// converts one immutable stopped HLS archive to a canonical MP4 media record.
// The job ID is the adapter idempotency key; implementations must return the
// same media ID after a process restart or an uncertain response.
type ArchiveImporter interface {
	ArchiveImportContract() string
	ArchiveImportReady(context.Context) error
	ImportHLSArchive(context.Context, ArchiveImportSource) (ArchiveImportReceipt, error)
}
type ArchiveImportSource struct {
	JobID, LeaseToken, ActorID, StreamID, OutputID, Namespace, Playlist, PlaylistSHA256 string
}
type ArchiveImportReceipt struct {
	MediaID, SourceSHA256 string
}
type ArchiveImport struct {
	ID        string    `json:"id"`
	StreamID  string    `json:"stream_id"`
	OutputID  string    `json:"output_id"`
	State     string    `json:"state"`
	MediaID   *string   `json:"media_id"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var outputProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func ValidOutputProviderID(id string) bool { return outputProviderIDPattern.MatchString(id) }
func ValidOutputLayout(s string) bool {
	switch s {
	case "grid", "speaker", "single-speaker", "grid-light", "speaker-light", "single-speaker-light":
		return true
	}
	return false
}
