// SPDX-License-Identifier: AGPL-3.0-only
package fortunes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type CorpusProvider struct{ corpus Corpus }

func NewCorpusProvider(c Corpus) (*CorpusProvider, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return nil, ErrInvalid
	}
	var copy Corpus
	if e = Decode(b, &copy); e != nil {
		return nil, e
	}
	if e = ValidateCorpus(&copy); e != nil {
		return nil, e
	}
	sort.Slice(copy.Reports, func(i, j int) bool { return copy.Reports[i].ID < copy.Reports[j].ID })
	return &CorpusProvider{copy}, nil
}
func (p *CorpusProvider) Revision() string { return "corpus." + p.corpus.Revision }
func (p *CorpusProvider) Generate(ctx context.Context, r Request) (Generation, error) {
	if e := ValidateRequest(r); e != nil {
		return Generation{}, e
	}
	if e := ctx.Err(); e != nil {
		return Generation{}, e
	}
	list := []Template{}
	for _, v := range p.corpus.Reports {
		if strings.EqualFold(v.Language, r.Language) && v.Level == r.Level {
			list = append(list, v)
		}
	}
	if len(list) == 0 {
		return Generation{}, ErrUnavailable
	}
	hash := sha256.Sum256([]byte("owned-fortune-v2\x00" + r.AccountID + "\x00" + r.Day + "\x00" + p.Revision()))
	v := list[binary.BigEndian.Uint64(hash[:8])%uint64(len(list))].Generation
	b, _ := json.Marshal(v)
	var out Generation
	e := json.Unmarshal(b, &out)
	return out, e
}

type HTTPProvider struct {
	endpoint, revision string
	client             *http.Client
}

// NewHTTPProvider configures an operator-selected HTTPS endpoint. No request is
// made here; no URL or credential is accepted through a user-facing endpoint.
func NewHTTPProvider(endpoint, revision string, client *http.Client) (*HTTPProvider, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !ValidRevision(revision) || client == nil {
		return nil, ErrInvalid
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout <= 0 || copy.Timeout > 15*time.Minute {
		copy.Timeout = 90 * time.Second
	}
	return &HTTPProvider{endpoint, revision, &copy}, nil
}
func (p *HTTPProvider) Revision() string { return "http." + p.revision }
func (p *HTTPProvider) Generate(ctx context.Context, v Request) (Generation, error) {
	var out Generation
	if e := ValidateRequest(v); e != nil {
		return out, e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return out, ErrInvalid
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(b))
	if e != nil {
		return out, ErrInvalid
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Idempotency-Key", v.IdempotencyKey)
	response, e := p.client.Do(r)
	if e != nil {
		return out, ErrProvider
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return out, ErrProvider
	}
	typ, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" {
		return out, ErrProvider
	}
	b, e = io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(b) > 65536 {
		return out, ErrProvider
	}
	if e = Decode(b, &out); e != nil {
		return out, ErrProvider
	}
	if e = ValidateGeneration(&out); e != nil {
		return Generation{}, ErrProvider
	}
	return out, nil
}
