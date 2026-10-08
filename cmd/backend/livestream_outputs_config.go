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
	"solsynth.local/go-migration/internal/rtcprovider"
	"solsynth.local/go-migration/ports/federation"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type outputSettings struct {
	enabled                     *bool
	file                        *string
	maximum, segments           *int
	playlistBytes, segmentBytes *int64
	timeout                     *time.Duration
}

func livestreamOutputFlags(fs *flag.FlagSet) outputSettings {
	return outputSettings{
		fs.Bool("enable-livestream-outputs", false, "enable explicitly configured egress and authorized HLS playback"),
		fs.String("livestream-output-config", "", "absolute private JSON configuration with fixed destination IDs and credential environment references"),
		fs.Int("livestream-output-limit", 100, "maximum retained output intents per stream"),
		fs.Int("livestream-hls-segment-limit", 1000, "maximum segments in a supported media playlist"),
		fs.Int64("livestream-hls-playlist-bytes", 256<<10, "maximum HLS playlist bytes"),
		fs.Int64("livestream-hls-segment-bytes", 8<<20, "maximum single MPEG-TS segment bytes"),
		fs.Duration("livestream-output-timeout", 3*time.Second, "bounded provider operation timeout"),
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
func (s outputSettings) load(streams bool, c *application.Config) (*rtcprovider.OutputPolicy, *livestreampg.OutputConfig, error) {
	invalid := errors.New("invalid or unavailable explicit livestream output configuration")
	defaults := *s.maximum == 100 && *s.segments == 1000 && *s.playlistBytes == 256<<10 && *s.segmentBytes == 8<<20 && *s.timeout == 3*time.Second
	if !*s.enabled {
		if *s.file != "" || !defaults {
			return nil, nil, invalid
		}
		return nil, nil, nil
	}
	if !streams || !filepath.IsAbs(*s.file) || filepath.Clean(*s.file) != *s.file {
		return nil, nil, invalid
	}
	info, e := os.Lstat(*s.file)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, nil, invalid
	}
	f, e := os.Open(*s.file)
	if e != nil {
		return nil, nil, invalid
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return nil, nil, invalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if e != nil || len(raw) > 64<<10 || federation.CheckJSON(raw) != nil {
		return nil, nil, invalid
	}
	var input outputFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Version != 1 || len(input.Destinations) < 1 || len(input.Destinations) > 32 {
		return nil, nil, invalid
	}
	policy := rtcprovider.OutputPolicy{Destinations: map[string]rtcprovider.OutputDestination{}}
	cfg := livestreampg.OutputConfig{MaxOutputsPerStream: *s.maximum, MaxSegments: *s.segments, MaxPlaylistBytes: *s.playlistBytes, MaxSegmentBytes: *s.segmentBytes, OperationTimeout: *s.timeout}
	if cfg.MaxOutputsPerStream < 1 || cfg.MaxOutputsPerStream > 10000 || cfg.MaxSegments < 1 || cfg.MaxSegments > 2000 || cfg.MaxPlaylistBytes < 1024 || cfg.MaxPlaylistBytes > 256<<10 || cfg.MaxSegmentBytes < 1024 || cfg.MaxSegmentBytes > 32<<20 || cfg.OperationTimeout <= 0 || cfg.OperationTimeout > 5*time.Second {
		return nil, nil, invalid
	}
	for _, v := range input.Destinations {
		if !live.ValidOutputDestination(v.ID) || policy.Destinations[v.ID].Kind != "" {
			return nil, nil, invalid
		}
		d := rtcprovider.OutputDestination{Kind: v.Kind}
		if v.Kind == "rtmp" {
			if v.Storage != nil {
				return nil, nil, invalid
			}
			var ok bool
			d.URL, ok = outputEnv(v.URLEnv)
			if !ok {
				return nil, nil, invalid
			}
		} else if v.Kind == "hls" || v.Kind == "file" {
			if v.Storage == nil || v.URLEnv != "" || !v.Storage.Private {
				return nil, nil, invalid
			}
			access, ok := outputEnv(v.Storage.AccessEnv)
			if !ok {
				return nil, nil, invalid
			}
			secret, ok := outputEnv(v.Storage.SecretEnv)
			if !ok {
				return nil, nil, invalid
			}
			session := ""
			if v.Storage.SessionEnv != "" {
				session, ok = outputEnv(v.Storage.SessionEnv)
				if !ok {
					return nil, nil, invalid
				}
			}
			d.Storage = &rtcprovider.OutputStorage{PrivateNamespace: true, AccessKey: access, Secret: secret, SessionToken: session, Bucket: v.Storage.Bucket, Region: v.Storage.Region, Endpoint: v.Storage.Endpoint, ForcePathStyle: v.Storage.PathStyle}
			if v.Kind == "hls" {
				cfg.HLSNamespaces = append(cfg.HLSNamespaces, v.ID)
			}
		} else {
			return nil, nil, invalid
		}
		policy.Destinations[v.ID] = d
	}
	if len(cfg.HLSNamespaces) > 0 {
		store, ok := c.MediaStore.(live.ArtifactStore)
		if !ok || cfg.MaxSegmentBytes > c.MaxMediaBytes {
			return nil, nil, invalid
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, ns := range cfg.HLSNamespaces {
			if e = store.StreamingReady(ctx, ns); e != nil {
				return nil, nil, invalid
			}
		}
	}
	return &policy, &cfg, nil
}
