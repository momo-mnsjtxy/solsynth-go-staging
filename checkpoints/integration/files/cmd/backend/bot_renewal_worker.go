// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

type botRenewalProcessor interface {
	ProcessBotEnrollmentRenewalsDue(context.Context, int) (int, error)
}

func runBotRenewalTicks(ctx context.Context, worker botRenewalProcessor, ticks <-chan time.Time, out io.Writer) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			job, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, e := worker.ProcessBotEnrollmentRenewalsDue(job, 100)
			cancel()
			if e != nil {
				fmt.Fprintln(out, "Bot enrollment renewal batch failed; consent remains durable.")
			}
		}
	}
}

func startBotRenewalWorker(ctx context.Context, worker botRenewalProcessor, interval time.Duration, out io.Writer) error {
	if worker == nil || interval < time.Second || interval > 30*time.Second {
		return errBotExecutionConfig
	}
	ticker := time.NewTicker(interval)
	go func() { defer ticker.Stop(); runBotRenewalTicks(ctx, worker, ticker.C, out) }()
	return nil
}
