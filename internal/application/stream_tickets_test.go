// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamTicketsSingleUseRevocationAndLimits(t *testing.T) {
	issuer, v, store := hostIdentity(t)
	token := hostToken(t, issuer, hostAccountA, "*")
	next := bindIdentity(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, e := (identity{v}).require(r.Context())
		if e != nil {
			writeAuthorityError(w, e)
			return
		}
		if p.Account.ID != hostAccountA || r.URL.Query().Has("ticket") {
			t.Error("wrong identity or credential retained")
		}
		w.WriteHeader(204)
	}))
	h := newStreamTickets(v, next)
	mint := func() string {
		t.Helper()
		w := hostRequest(h, "POST", "/api/realtime/tickets", token, "")
		if w.Code != 200 {
			t.Fatal("mint", w.Code)
		}
		var reply struct {
			Ticket string `json:"ticket"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &reply); e != nil || len(reply.Ticket) != 43 {
			t.Fatal("ticket shape", e)
		}
		return reply.Ticket
	}
	ticket := mint()
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := hostRequest(h, "GET", "/ws?ticket="+ticket, "", "")
			if w.Code == 204 {
				won.Add(1)
			} else if w.Code != 401 {
				t.Error("redeem", w.Code)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("ticket reused", won.Load())
	}
	ticket = mint()
	if w := hostRequest(h, "GET", "/ws?ticket="+ticket, token, ""); w.Code != 400 {
		t.Fatal("ambiguous credentials", w.Code)
	}
	if w := hostRequest(h, "GET", "/ws?ticket="+ticket+"&ticket="+ticket, "", ""); w.Code != 400 {
		t.Fatal("duplicate ticket", w.Code)
	}
	now := time.Now()
	h.now = func() time.Time { return now }
	ticket = mint()
	now = now.Add(31 * time.Second)
	if w := hostRequest(h, "GET", "/ws?ticket="+ticket, "", ""); w.Code != 401 {
		t.Fatal("expired ticket", w.Code)
	}
	h.now = time.Now
	h.mu.Lock()
	h.entries = map[[32]byte]streamTicket{}
	h.mu.Unlock()
	for range 4 {
		mint()
	}
	if w := hostRequest(h, "POST", "/api/realtime/tickets", token, ""); w.Code != 429 {
		t.Fatal("ticket capacity", w.Code)
	}
	h.mu.Lock()
	h.entries = map[[32]byte]streamTicket{}
	h.mu.Unlock()
	ticket = mint()
	store.mu.Lock()
	store.accounts[hostAccountA].account.Version++
	store.mu.Unlock()
	if w := hostRequest(h, "GET", "/ws?ticket="+ticket, "", ""); w.Code != 401 {
		t.Fatal("revoked ticket proof", w.Code)
	}
	if w := hostRequest(h, "POST", "/api/realtime/tickets", "", ""); w.Code != 401 {
		t.Fatal("anonymous mint", w.Code)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/realtime/tickets", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
