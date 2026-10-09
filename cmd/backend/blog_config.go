// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"solsynth.local/go-migration/internal/blogpg"
	"solsynth.local/go-migration/internal/readerfetch"
	"solsynth.local/go-migration/ports/sphere/reader"
	"strings"
	"time"
)

type blogSettings struct {
	enabled, verify *bool
	origins         *string
}

func blogFlags(f *flag.FlagSet) blogSettings {
	return blogSettings{f.Bool("enable-blogs", false, "enable owned blog and publisher-domain workflows"), f.Bool("blog-domain-verify", false, "explicitly enable public HTTPS domain verification"), f.String("blog-domain-origins", "", "comma-separated exact HTTPS origins allowed for domain verification")}
}
func (s blogSettings) config() (*blogpg.Config, error) {
	if !*s.enabled {
		if *s.verify || *s.origins != "" {
			return nil, errors.New("blog settings require explicit enablement")
		}
		return nil, nil
	}
	c := &blogpg.Config{}
	if !*s.verify {
		if *s.origins != "" {
			return nil, errors.New("domain origins require explicit verification enablement")
		}
		return c, nil
	}
	origins := strings.Split(*s.origins, ",")
	for i := range origins {
		origins[i] = strings.TrimSpace(origins[i])
	}
	p, e := reader.NewPolicy(origins)
	if e != nil {
		return nil, e
	}
	c.Verifier, e = readerfetch.New(readerfetch.Config{Policy: p, Timeout: 10 * time.Second, MaxBytes: 65536, MaxConcurrent: 4, MaxRedirects: 0})
	return c, e
}
