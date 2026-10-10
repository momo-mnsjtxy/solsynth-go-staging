// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
	"testing"
	"time"
)

func TestSyncBoundaryPreservesTimestampBucketsAndFailsOverflow(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	values := []time.Time{}
	for i := 0; i < 750; i++ {
		values = append(values, at)
	}
	values = append(values, at.Add(time.Millisecond))
	got, e := syncBoundary(values, at.Add(time.Second))
	if e != nil || !got.Equal(at) {
		t.Fatal(got, e)
	}
	emitted := 0
	for _, v := range values {
		if !v.After(got) {
			emitted++
		}
	}
	if emitted != 750 {
		t.Fatal("lost tied rows", emitted)
	}
	got, e = syncBoundary(make([]time.Time, 10001), at)
	if !errors.Is(e, rooms.ErrSyncBucketOverflow) || !got.IsZero() {
		t.Fatal("advanced overflow", got, e)
	}
}
