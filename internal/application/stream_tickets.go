// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"sync"
	"time"
)

type streamTicket struct {
	proof   authority.Principal
	expires time.Time
}
type streamTickets struct {
	db       *sql.DB
	mu       sync.Mutex
	entries  map[[32]byte]streamTicket
	verifier *authority.Verifier
	now      func() time.Time
	next     http.Handler
}

// newStreamTickets is the process-local test implementation. Production uses
// newSharedStreamTickets so a ticket survives issuer restart and replica hops.
// Only a hash and original opaque proof are retained, never bearer bytes.
func newStreamTickets(v *authority.Verifier, next http.Handler) *streamTickets {
	return &streamTickets{entries: map[[32]byte]streamTicket{}, verifier: v, now: time.Now, next: next}
}

func newSharedStreamTickets(db *sql.DB, v *authority.Verifier, next http.Handler) *streamTickets {
	return &streamTickets{db: db, verifier: v, now: time.Now, next: next}
}
func (t *streamTickets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/realtime/tickets" {
		bindIdentity(t.verifier, http.HandlerFunc(t.issue)).ServeHTTP(w, r)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		problem(w, 400, "INVALID_STREAM_QUERY")
		return
	}
	values, present := q["ticket"]
	if !present {
		t.next.ServeHTTP(w, r)
		return
	}
	if (r.URL.Path != "/ws" && r.URL.Path != "/api/realtime/events") || r.Method != "GET" || len(values) != 1 || len(values[0]) != 43 {
		problem(w, 400, "INVALID_STREAM_TICKET")
		return
	}
	for k := range r.Header {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Cookie") {
			problem(w, 400, "CONFLICTING_STREAM_IDENTITY")
			return
		}
	}
	key := sha256.Sum256([]byte(values[0]))
	var proof authority.Principal
	if t.db == nil {
		t.mu.Lock()
		entry, ok := t.entries[key]
		delete(t.entries, key)
		t.mu.Unlock()
		if !ok || !t.now().Before(entry.expires) {
			problem(w, 401, "STREAM_TICKET_EXPIRED_OR_USED")
			return
		}
		proof, err = t.verifier.AuthenticatePrincipal(r.Context(), entry.proof)
	} else {
		proof, err = t.redeem(r.Context(), key[:])
	}
	if err != nil {
		writeAuthorityError(w, err)
		return
	}
	q.Del("ticket")
	copy := r.Clone(context.WithValue(r.Context(), principalKey{}, proof))
	u := *r.URL
	u.RawQuery = q.Encode()
	copy.URL = &u
	copy.RequestURI = u.RequestURI()
	t.next.ServeHTTP(w, copy)
}
func (t *streamTickets) issue(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		problem(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	if r.ContentLength != 0 || r.URL.RawQuery != "" {
		problem(w, 400, "INVALID_STREAM_TICKET_REQUEST")
		return
	}
	proof, err := (identity{t.verifier}).require(r.Context())
	if err != nil {
		writeAuthorityError(w, err)
		return
	}
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		problem(w, 503, "STREAM_TICKET_UNAVAILABLE")
		return
	}
	value := base64.RawURLEncoding.EncodeToString(raw[:])
	key := sha256.Sum256([]byte(value))
	now := t.now().UTC()
	if t.db != nil {
		if err = t.insert(r.Context(), key[:], proof); err != nil {
			if errors.Is(err, errStreamTicketLimit) {
				problem(w, 429, "STREAM_TICKET_LIMIT")
			} else {
				problem(w, 503, "STREAM_TICKET_UNAVAILABLE")
			}
			return
		}
	} else {
		count := 0
		t.mu.Lock()
		for k, v := range t.entries {
			if !now.Before(v.expires) {
				delete(t.entries, k)
			} else if v.proof.Account.ID == proof.Account.ID {
				count++
			}
		}
		if len(t.entries) >= 256 || count >= 4 {
			t.mu.Unlock()
			problem(w, 429, "STREAM_TICKET_LIMIT")
			return
		}
		t.entries[key] = streamTicket{proof: proof, expires: now.Add(30 * time.Second)}
		t.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{"ticket": value, "expires_in": 30})
}

var errStreamTicketLimit = errors.New("stream ticket limit")

func (t *streamTickets) insert(ctx context.Context, hash []byte, proof authority.Principal) error {
	use := "user"
	if proof.Session.Type == 1 && proof.Session.Category == authority.OAuthSessionCategory {
		use = authority.OAuthSessionCategory
	} else if proof.Session.Type != 0 || proof.Session.Category == authority.OAuthSessionCategory {
		return authority.ErrInvalid
	}
	tx, err := t.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('owned-authority-stream-tickets',0))`).Scan(&locked); err != nil || !locked {
		if err == nil {
			err = errStreamTicketLimit
		}
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM owned_authority_stream_tickets WHERE expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	var total, account int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE account_id=$1) FROM owned_authority_stream_tickets`, proof.Account.ID).Scan(&total, &account); err != nil {
		return err
	}
	if total >= 256 || account >= 4 {
		return errStreamTicketLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owned_authority_stream_tickets(ticket_hash,account_id,session_id,account_version,session_epoch,authorization_epoch,credential_use,issued_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp(),clock_timestamp()+interval '30 seconds')`, hash, proof.Account.ID, proof.Session.ID, proof.Account.Version, proof.Session.Epoch, proof.Session.AuthorizationEpoch, use)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (t *streamTickets) redeem(ctx context.Context, hash []byte) (authority.Principal, error) {
	return t.verifier.RedeemBrowserTicket(ctx, hash)
}
