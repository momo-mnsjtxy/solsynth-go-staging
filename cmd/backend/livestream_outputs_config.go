// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/rtcprovider"
	"solsynth.local/go-migration/ports/federation"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type outputSettings struct {
	enabled, archive            *bool
	file, ffmpeg, limiter       *string
	maximum, segments           *int
	playlistBytes, segmentBytes *int64
	archiveInput, archiveOutput *int64
	timeout, archiveTimeout     *time.Duration
}

func livestreamOutputFlags(fs *flag.FlagSet) outputSettings {
	return outputSettings{
		enabled:        fs.Bool("enable-livestream-outputs", false, "enable explicitly configured egress and authorized HLS playback"),
		archive:        fs.Bool("enable-livestream-archive-import", false, "enable bounded stopped-HLS conversion into canonical private MP4 media"),
		file:           fs.String("livestream-output-config", "", "absolute private JSON configuration with fixed destination IDs and credential environment references"),
		ffmpeg:         fs.String("livestream-archive-ffmpeg", "", "explicit absolute ffmpeg executable for HLS archive conversion"),
		limiter:        fs.String("livestream-archive-limiter", "", "explicit absolute Linux prlimit executable for HLS archive conversion"),
		maximum:        fs.Int("livestream-output-limit", 100, "maximum retained output intents per stream"),
		segments:       fs.Int("livestream-hls-segment-limit", 1000, "maximum segments in a supported media playlist"),
		playlistBytes:  fs.Int64("livestream-hls-playlist-bytes", 256<<10, "maximum HLS playlist bytes"),
		segmentBytes:   fs.Int64("livestream-hls-segment-bytes", 8<<20, "maximum single MPEG-TS segment bytes"),
		archiveInput:   fs.Int64("livestream-archive-input-bytes", 1<<30, "maximum total HLS archive input bytes"),
		archiveOutput:  fs.Int64("livestream-archive-output-bytes", 1<<30, "maximum canonical MP4 output bytes"),
		timeout:        fs.Duration("livestream-output-timeout", 3*time.Second, "bounded provider operation timeout"),
		archiveTimeout: fs.Duration("livestream-archive-timeout", 2*time.Minute, "bounded HLS archive conversion timeout"),
	}
}

type outputFile struct {
	Version      int `json:"version"`
	Destinations []struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		URLEnv  string `json:"url_env"`
		Storage *struct {
			Private    bool   `json:"private_namespace"`
			Bucket     string `json:"bucket"`
			Region     string `json:"region"`
			Endpoint   string `json:"endpoint"`
			PathStyle  bool   `json:"force_path_style"`
			AccessEnv  string `json:"access_key_env"`
			SecretEnv  string `json:"secret_env"`
			SessionEnv string `json:"session_token_env"`
		} `json:"storage"`
	} `json:"destinations"`
}

var outputEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func outputEnv(name string) (string, bool) {
	if !outputEnvironmentName.MatchString(name) {
		return "", false
	}
	value, ok := os.LookupEnv(name)
	return value, ok && value != ""
}
func (s outputSettings) load(streams bool, c *application.Config) (*rtcprovider.OutputPolicy, *livestreampg.OutputConfig, *mediapg.ArchiveImportConfig, error) {
	invalid := errors.New("invalid or unavailable explicit livestream output configuration")
	archiveDefaults := *s.ffmpeg == "" && *s.limiter == "" && *s.archiveInput == 1<<30 && *s.archiveOutput == 1<<30 && *s.archiveTimeout == 2*time.Minute
	defaults := *s.maximum == 100 && *s.segments == 1000 && *s.playlistBytes == 256<<10 && *s.segmentBytes == 8<<20 && *s.timeout == 3*time.Second && !*s.archive && archiveDefaults
	if !*s.enabled {
		if *s.file != "" || !defaults {
			return nil, nil, nil, invalid
		}
		return nil, nil, nil, nil
	}
	if !streams || !filepath.IsAbs(*s.file) || filepath.Clean(*s.file) != *s.file {
		return nil, nil, nil, invalid
	}
	info, e := os.Lstat(*s.file)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, nil, nil, invalid
	}
	f, e := os.Open(*s.file)
	if e != nil {
		return nil, nil, nil, invalid
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return nil, nil, nil, invalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if e != nil || len(raw) > 64<<10 || federation.CheckJSON(raw) != nil {
		return nil, nil, nil, invalid
	}
	var input outputFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Version != 1 || len(input.Destinations) < 1 || len(input.Destinations) > 32 {
		return nil, nil, nil, invalid
	}
	policy := rtcprovider.OutputPolicy{Destinations: map[string]rtcprovider.OutputDestination{}}
	cfg := livestreampg.OutputConfig{MaxOutputsPerStream: *s.maximum, MaxSegments: *s.segments, MaxPlaylistBytes: *s.playlistBytes, MaxSegmentBytes: *s.segmentBytes, OperationTimeout: *s.timeout}
	if cfg.MaxOutputsPerStream < 1 || cfg.MaxOutputsPerStream > 10000 || cfg.MaxSegments < 1 || cfg.MaxSegments > 2000 || cfg.MaxPlaylistBytes < 1024 || cfg.MaxPlaylistBytes > 256<<10 || cfg.MaxSegmentBytes < 1024 || cfg.MaxSegmentBytes > 32<<20 || cfg.OperationTimeout <= 0 || cfg.OperationTimeout > 5*time.Second {
		return nil, nil, nil, invalid
	}
	for _, v := range input.Destinations {
		if !live.ValidOutputDestination(v.ID) || policy.Destinations[v.ID].Kind != "" {
			return nil, nil, nil, invalid
		}
		d := rtcprovider.OutputDestination{Kind: v.Kind}
		if v.Kind == "rtmp" {
			if v.Storage != nil {
				return nil, nil, nil, invalid
			}
			var ok bool
			d.URL, ok = outputEnv(v.URLEnv)
			if !ok {
				return nil, nil, nil, invalid
			}
		} else if v.Kind == "hls" || v.Kind == "file" {
			if v.Storage == nil || v.URLEnv != "" || !v.Storage.Private {
				return nil, nil, nil, invalid
			}
			access, ok := outputEnv(v.Storage.AccessEnv)
			if !ok {
				return nil, nil, nil, invalid
			}
			secret, ok := outputEnv(v.Storage.SecretEnv)
			if !ok {
				return nil, nil, nil, invalid
			}
			session := ""
			if v.Storage.SessionEnv != "" {
				session, ok = outputEnv(v.Storage.SessionEnv)
				if !ok {
					return nil, nil, nil, invalid
				}
			}
			d.Storage = &rtcprovider.OutputStorage{PrivateNamespace: true, AccessKey: access, Secret: secret, SessionToken: session, Bucket: v.Storage.Bucket, Region: v.Storage.Region, Endpoint: v.Storage.Endpoint, ForcePathStyle: v.Storage.PathStyle}
			if v.Kind == "hls" {
				cfg.HLSNamespaces = append(cfg.HLSNamespaces, v.ID)
			}
		} else {
			return nil, nil, nil, invalid
		}
		policy.Destinations[v.ID] = d
	}
	if len(cfg.HLSNamespaces) > 0 {
		store, ok := c.MediaStore.(live.ArtifactStore)
		if !ok || cfg.MaxSegmentBytes > c.MaxMediaBytes {
			return nil, nil, nil, invalid
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, ns := range cfg.HLSNamespaces {
			if e = store.StreamingReady(ctx, ns); e != nil {
				return nil, nil, nil, invalid
			}
		}
	}
	var archive *mediapg.ArchiveImportConfig
	if *s.archive {
		if *s.ffmpeg == "" || *s.limiter == "" || len(cfg.HLSNamespaces) == 0 || *s.archiveInput < cfg.MaxSegmentBytes || *s.archiveInput > 2<<30 || *s.archiveOutput < 1024 || *s.archiveOutput > c.MaxMediaBytes || *s.archiveTimeout < 5*time.Second || *s.archiveTimeout > 5*time.Minute {
			return nil, nil, nil, invalid
		}
		archive = &mediapg.ArchiveImportConfig{FFmpeg: *s.ffmpeg, Limiter: *s.limiter, MaxSegments: cfg.MaxSegments, MaxPlaylistBytes: cfg.MaxPlaylistBytes, MaxSegmentBytes: cfg.MaxSegmentBytes, MaxInputBytes: *s.archiveInput, MaxOutputBytes: *s.archiveOutput, Timeout: *s.archiveTimeout}
	} else if !archiveDefaults {
		return nil, nil, nil, invalid
	}
	return &policy, &cfg, archive, nil
}
