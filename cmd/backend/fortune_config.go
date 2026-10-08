// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"time"
)

type fortuneSettings struct {
	enabled, anonymous         *bool
	corpus, endpoint, revision *string
	interval                   *time.Duration
}

func fortuneFlags(fs *flag.FlagSet) fortuneSettings {
	return fortuneSettings{
		fs.Bool("enable-fortunes", false, "enable explicitly configured fortune catalogue/report jobs"),
		fs.Bool("fortune-anonymous-catalogue", false, "explicitly allow public configured sayings"),
		fs.String("fortune-corpus-file", "", "operator-provided JSON corpus with explicit provenance"),
		fs.String("fortune-provider-url", "", "operator-selected HTTPS report provider endpoint"),
		fs.String("fortune-provider-revision", "", "immutable configured report provider revision"),
		fs.Duration("fortune-interval", 5*time.Second, "one durable report job per bounded interval"),
	}
}
func (s fortuneSettings) config() (cfg *fortunepg.Config, corpus *fortunes.Corpus, e error) {
	if !*s.enabled {
		if *s.anonymous || *s.corpus != "" || *s.endpoint != "" || *s.revision != "" {
			return nil, nil, errors.New("fortune settings require explicit enablement")
		}
		return nil, nil, nil
	}
	if *s.interval < time.Second || *s.interval > time.Hour {
		return nil, nil, errors.New("invalid fortune interval")
	}
	cfg = &fortunepg.Config{AllowAnonymousCatalogue: *s.anonymous}
	if *s.endpoint != "" {
		cfg.Provider, e = fortunes.NewHTTPProvider(*s.endpoint, *s.revision, &http.Client{Timeout: 90 * time.Second})
		if e != nil {
			return nil, nil, e
		}
	} else if *s.revision != "" {
		return nil, nil, errors.New("fortune provider revision requires endpoint")
	}
	if *s.corpus != "" {
		f, err := os.Open(*s.corpus)
		if err != nil {
			return nil, nil, errors.New("fortune corpus unavailable")
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
		if err != nil || len(raw) > 2<<20 {
			return nil, nil, errors.New("fortune corpus too large or unreadable")
		}
		corpus = &fortunes.Corpus{}
		if err = fortunes.Decode(raw, corpus); err != nil {
			return nil, nil, err
		}
		if err = fortunes.ValidateCorpus(corpus); err != nil {
			return nil, nil, err
		}
	}
	if corpus == nil && cfg.Provider == nil {
		return nil, nil, errors.New("explicit fortune corpus or provider required")
	}
	return
}
func (s fortuneSettings) start(ctx context.Context, b *application.Backend, out io.Writer) {
	if !*s.enabled {
		return
	}
	go func() {
		ticker := time.NewTicker(*s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				job, cancel := context.WithTimeout(ctx, 2*time.Minute)
				_, err := b.ProcessFortune(job)
				cancel()
				if err != nil {
					io.WriteString(out, "Fortune report job failed; retry state remains durable.\n")
				}
			}
		}
	}()
}
