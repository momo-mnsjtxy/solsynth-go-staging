// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type renewalWorkerFixture struct {
	calls atomic.Int32
	done  chan struct{}
	want  int32
}

func (f *renewalWorkerFixture) ProcessBotEnrollmentRenewalsDue(context.Context, int) (int, error) {
	if f.calls.Add(1) == f.want {
		close(f.done)
	}
	return 1, nil
}
func TestBotRenewalDedicatedCadenceBoundary(t *testing.T) {
	for _, d := range []time.Duration{time.Second, 30 * time.Second} {
		ctx, stop := context.WithCancel(context.Background())
		if e := startBotRenewalWorker(ctx, &renewalWorkerFixture{want: 1}, d, &bytes.Buffer{}); e != nil {
			t.Fatal(d, e)
		}
		stop()
	}
	for _, d := range []time.Duration{time.Second - time.Nanosecond, 30*time.Second + time.Nanosecond} {
		if e := startBotRenewalWorker(context.Background(), &renewalWorkerFixture{want: 1}, d, &bytes.Buffer{}); e == nil {
			t.Fatal("accepted", d)
		}
	}
	actualCtx, actualCancel := context.WithCancel(context.Background())
	actual := &renewalWorkerFixture{done: make(chan struct{}), want: 1}
	if e := startBotRenewalWorker(actualCtx, actual, time.Second, &bytes.Buffer{}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-actual.done:
		actualCancel()
	case <-time.After(2500 * time.Millisecond):
		actualCancel()
		t.Fatal("one-second renewal ticker did not run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 2)
	f := &renewalWorkerFixture{done: make(chan struct{}), want: 2}
	go runBotRenewalTicks(ctx, f, ticks, &bytes.Buffer{})
	ticks <- time.Now()
	ticks <- time.Now()
	select {
	case <-f.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not consume independent ticks")
	}
}
