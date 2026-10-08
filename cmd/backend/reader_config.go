// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"solsynth.local/go-migration/internal/readerfetch"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"time"
)

type readerSettings struct {
	enabled, fetch *bool
	origins        *string
}

func readerFlags(f *flag.FlagSet) readerSettings {
	return readerSettings{f.Bool("enable-reader", false, "enable explicitly bounded preview cache"), f.Bool("reader-fetch", false, "explicitly enable outbound public HTTPS preview fetches"), f.String("reader-origins", "", "comma-separated exact allowed HTTPS origins")}
}
func (s readerSettings) config() (*readerpg.Config, error) {
	if !*s.enabled {
		if *s.fetch || *s.origins != "" {
			return nil, errors.New("reader settings require explicit enablement")
		}
		return nil, nil
	}
	origins := strings.Split(*s.origins, ",")
	for i := range origins {
		origins[i] = strings.TrimSpace(origins[i])
	}
	policy, e := reader.NewPolicy(origins)
	if e != nil {
		return nil, e
	}
	c := &readerpg.Config{Policy: policy, TTL: 24 * time.Hour, MaxEntries: 5000}
	if *s.fetch {
		c.Fetcher, e = readerfetch.New(readerfetch.Config{Policy: policy, Timeout: 3 * time.Second, MaxBytes: 2 << 20, MaxConcurrent: 8, MaxRedirects: 3})
		if e != nil {
			return nil, e
		}
	}
	return c, nil
}
