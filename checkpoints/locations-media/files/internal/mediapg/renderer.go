// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"solsynth.local/go-migration/ports/media"
	"time"
)

type FFmpegRenderer struct {
	executable, limiter string
	slots               chan struct{}
}

// Explicit Linux executables are required. prlimit enforces process-wide
// address-space/CPU limits in addition to ffmpeg's bounded allocations, fixed
// formats/codecs, pipe-only protocol and one-frame PNG output.
// https://ffmpeg.org/ffmpeg.html; https://ffmpeg.org/ffmpeg-protocols.html
func NewFFmpegRenderer(executable, limiter string) (*FFmpegRenderer, error) {
	if runtime.GOOS != "linux" {
		return nil, media.ErrUnavailable
	}
	for _, path := range []string{executable, limiter} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, media.ErrInvalid
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil || real != path {
			return nil, media.ErrInvalid
		}
		info, e := os.Stat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
			return nil, media.ErrInvalid
		}
	}
	return &FFmpegRenderer{executable, limiter, make(chan struct{}, 2)}, nil
}

type boundedRenderOutput struct{ bytes.Buffer }

func (w *boundedRenderOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 1<<20 {
		return 0, errors.New("bounded renderer output exceeded")
	}
	return w.Buffer.Write(p)
}
func (r *FFmpegRenderer) RenderPNG(ctx context.Context, mime string, input []byte) ([]byte, error) {
	if r == nil || len(input) == 0 || len(input) > 20<<20 {
		return nil, media.ErrTooLarge
	}
	format, codecs := "", ""
	switch mime {
	case "image/avif":
		format, codecs = "mov", "av1,libdav1d,libaom-av1"
	case "video/mp4":
		format, codecs = "mov", "h264,hevc,av1,libdav1d,libaom-av1,mjpeg"
	case "video/webm":
		format, codecs = "matroska", "vp8,vp9,av1,libdav1d,libaom-av1"
	default:
		return nil, media.ErrUnsupported
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		return nil, media.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Uploaded bytes never become command text, paths, flags or network URLs.
	args := []string{"--as=536870912", "--cpu=4", "--fsize=1048576", "--", r.executable, "-nostdin", "-hide_banner", "-loglevel", "error", "-max_alloc", "67108864", "-threads", "1", "-protocol_whitelist", "pipe", "-format_whitelist", format, "-codec_whitelist", codecs, "-f", format, "-i", "pipe:0", "-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", "1", "-filter_threads", "1", "-vf", "scale=256:256:force_original_aspect_ratio=decrease", "-threads", "1", "-c:v", "png", "-f", "image2pipe", "pipe:1"}
	cmd := exec.CommandContext(ctx, r.limiter, args...)
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(input)
	var out boundedRenderOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if e := cmd.Run(); e != nil {
		return nil, media.ErrInvalid
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(out.Bytes()))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 256 || cfg.Height > 256 {
		return nil, media.ErrInvalid
	}
	return out.Bytes(), nil
}
