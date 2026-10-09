// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type archiveTestStore struct{ root string }

func (archiveTestStore) StreamingReady(context.Context, string) error { return nil }
func (s archiveTestStore) OpenStreamingArtifact(_ context.Context, namespace, output, asset string, limit int64) (io.ReadSeekCloser, int64, time.Time, error) {
	if namespace != "synthetic" {
		return nil, 0, time.Time{}, live.ErrNotFound
	}
	f, e := os.Open(filepath.Join(s.root, output, asset))
	if e != nil {
		return nil, 0, time.Time{}, e
	}
	info, e := f.Stat()
	if e != nil || info.Size() > limit {
		f.Close()
		return nil, 0, time.Time{}, live.ErrInvalid
	}
	return f, info.Size(), info.ModTime(), nil
}

func TestArchiveImporterRunsBoundedLocalFFmpeg(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("ffmpeg is not installed")
	}
	limiter, e := exec.LookPath("prlimit")
	if e != nil {
		t.Skip("prlimit is not installed")
	}
	ffmpeg, _ = filepath.Abs(ffmpeg)
	limiter, _ = filepath.Abs(limiter)
	root := t.TempDir()
	output := "00000000-0000-4000-8000-000000000002"
	dir := filepath.Join(root, output)
	if e = os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	segment := filepath.Join(dir, "segment_0.ts")
	generate := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=1", "-t", "1", "-c:v", "libx264", "-threads", "1", "-f", "mpegts", segment)
	if raw, x := generate.CombinedOutput(); x != nil {
		t.Skipf("ffmpeg lacks the test H.264 encoder: %v: %s", x, raw)
	}
	playlist := []byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\nsegment_0.ts\n#EXT-X-ENDLIST\n")
	if e = os.WriteFile(filepath.Join(dir, "playlist.m3u8"), playlist, 0600); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(playlist)
	importer := &archiveImporter{artifacts: archiveTestStore{root}, cfg: ArchiveImportConfig{FFmpeg: ffmpeg, Limiter: limiter, MaxSegments: 10, MaxPlaylistBytes: 64 << 10, MaxSegmentBytes: 4 << 20, MaxInputBytes: 8 << 20, MaxOutputBytes: 8 << 20, Timeout: 15 * time.Second}, slots: make(chan struct{}, 2)}
	path, sourceHash, size, e := importer.transcode(context.Background(), live.ArchiveImportSource{StreamID: "00000000-0000-4000-8000-000000000001", OutputID: output, Namespace: "synthetic", Playlist: "playlist.m3u8", PlaylistSHA256: hex.EncodeToString(digest[:])})
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(filepath.Dir(path))
	raw, e := os.ReadFile(path)
	if e != nil || size != int64(len(raw)) || len(raw) < 12 || string(raw[4:8]) != "ftyp" || len(sourceHash) != 64 {
		t.Fatal("bounded archive conversion did not produce an MP4 receipt", size, sourceHash, e)
	}
}
