// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"strings"
	"testing"
)

func TestHLSProjectionReferencesOnlyValidatedOwnedSegments(t *testing.T) {
	const stream = "a1000000-0000-4000-8000-000000000001"
	const output = "a1000000-0000-4000-8000-000000000002"
	input := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:20\n#EXTINF:5.7,\nsegment_20.ts\n#EXT-X-DISCONTINUITY\n#EXT-X-PROGRAM-DATE-TIME:2026-10-08T12:00:00Z\n#EXTINF:6,\nsegment_21.ts\n#EXT-X-ENDLIST\n"
	out, files, e := ProjectHLS([]byte(input), stream, output, 2)
	if e != nil || len(files) != 2 || !files["segment_20.ts"] || !strings.Contains(string(out), "/api/livestreams/"+stream+"/hls/"+output+"/segment_20.ts") {
		t.Fatal(string(out), files, e)
	}
	for _, segment := range []string{"../secret.ts", "/private.ts", "https://external.invalid/leak.ts", "//external.invalid/leak.ts", "a%2fts.ts", "key.bin", "nested/a.ts", "a\\b.ts", "a.ts?token=secret", "a.ts#fragment"} {
		bad := strings.Replace(input, "segment_20.ts", segment, 1)
		if _, _, e := ProjectHLS([]byte(bad), stream, output, 2); e == nil {
			t.Fatal("unsafe reference accepted", segment)
		}
	}
	for _, extra := range []string{"#EXT-X-KEY:METHOD=AES-128,URI=\"https://external.invalid/key\"\n", "#EXT-X-MAP:URI=\"init.mp4\"\n", "#EXT-X-BYTERANGE:100@0\n", "#EXT-X-STREAM-INF:BANDWIDTH=1\n", "#EXT-X-SESSION-DATA:DATA-ID=\"external\"\n", "#unknown\n"} {
		bad := strings.Replace(input, "#EXTINF:5.7,", extra+"#EXTINF:5.7,", 1)
		if _, _, e := ProjectHLS([]byte(bad), stream, output, 2); e == nil {
			t.Fatal("unsupported tag accepted", extra)
		}
	}
	for _, bad := range []string{strings.Replace(input, "5.7", "NaN", 1), strings.Replace(input, "5.7", "1000", 1), strings.Replace(input, "segment_21.ts", "segment_20.ts", 1), input + "#EXTINF:1,\nextra.ts\n", strings.Replace(input, "#EXTM3U", "#EXTM3U\x00", 1)} {
		if _, _, e := ProjectHLS([]byte(bad), stream, output, 2); e == nil {
			t.Fatal("malformed media playlist accepted")
		}
	}
	if _, _, e := ProjectHLS([]byte(input), stream, output, 1); e == nil {
		t.Fatal("segment bound ignored")
	}
}
