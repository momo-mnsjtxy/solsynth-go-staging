// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/messager"
)

// Process tests run only after database(t) has checked the marked disposable
// cluster. Port zero is bound by the actual server; no release/rebind race and
// no attempt to contact an arbitrary endpoint from a fixture or environment.
type domainProcess struct {
	base   string
	client *http.Client
	stop   func()
}

func processReadinessAddress(line, marker string) (string, bool, error) {
	_, address, found := strings.Cut(line, marker)
	if !found {
		return "", false, nil
	}
	address = strings.TrimSpace(address)
	host, port, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	number, parseErr := strconv.ParseUint(port, 10, 16)
	if err != nil || ip == nil || !ip.IsLoopback() || parseErr != nil || number == 0 {
		return "", true, fmt.Errorf("invalid process readiness address")
	}
	return "http://" + address, true, nil
}
func TestProcessReadinessAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:1234", "[::1]:5678"} {
		base, found, err := processReadinessAddress("prefix listening on "+address, "listening on ")
		if err != nil || !found || base != "http://"+address {
			t.Fatal(base, found, err)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "0.0.0.0:1234", "example.invalid:443", "127.0.0.1:99999", "127.0.0.1:1234/path"} {
		if _, _, err := processReadinessAddress("listening on "+address, "listening on "); err == nil {
			t.Fatal(address)
		}
	}
	if _, found, err := processReadinessAddress("unrelated", "listening on "); found || err != nil {
		t.Fatal(found, err)
	}
}
func startDomainProcess(t *testing.T, binaryEnv, marker string, args ...string) *domainProcess {
	t.Helper()
	database(t)
	binary := os.Getenv(binaryEnv)
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatalf("%s must contain the absolute built fixture binary", binaryEnv)
	}
	command := exec.Command(binary, append([]string{"--listen=127.0.0.1:0"}, args...)...)
	command.Env = os.Environ()
	command.Stdout = io.Discard
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = command.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				<-done
			}
		})
	}
	t.Cleanup(stop)
	type readiness struct {
		base string
		err  error
	}
	ready := make(chan readiness, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		sent := false
		for scanner.Scan() {
			base, found, err := processReadinessAddress(scanner.Text(), marker)
			if found && !sent {
				ready <- readiness{base, err}
				sent = true
			}
		}
		if !sent {
			ready <- readiness{err: fmt.Errorf("process exited without readiness")}
		}
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case result := <-ready:
		if result.err != nil {
			stop()
			t.Fatal(result.err)
		}
		transport := &http.Transport{Proxy: nil}
		t.Cleanup(transport.CloseIdleConnections)
		return &domainProcess{base: result.base, client: &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, stop: stop}
	case <-done:
		t.Fatal("fixture process exited before readiness")
	case <-timer.C:
		stop()
		t.Fatal("fixture process readiness timeout")
	}
	return nil
}
func (p *domainProcess) call(t *testing.T, method, path, body string, status int, headers map[string]string) ([]byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, p.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 || response.StatusCode != status {
		t.Fatalf("%s %s: status=%d body=%s error=%v", method, path, response.StatusCode, raw, err)
	}
	return raw, response.Header
}

func TestPGMessagerHTTPProcessRestart(t *testing.T) {
	db := database(t)
	room, sender := messagerFixture(t, db, 60, false, 88)
	messagerInsert(t, db, 60, 10, room, sender, 9007199254740993, messagerBase(), "messages.update")
	// Encryption data and ciphertext survive real HTTP unchanged; no decoder or
	// plaintext formatter may interpret them, including exact large JSON numbers.
	query(t, db, `UPDATE chat_messages SET content=$1,encryption_meta=$2::jsonb WHERE id=$3`, "# opaque-ciphertext", `{"version":9007199254740993,"nonce":"synthetic"}`, messagerID(60, 10))
	path := "/api/chat/" + room + "/sync"
	disabled := startDomainProcess(t, "MESSAGER_SYNC_BINARY", "Messager sync listening on ")
	disabled.call(t, "POST", path, `{"last_sync_timestamp":0}`, 503, nil)
	disabled.stop()
	for i := 0; i < 2; i++ {
		process := startDomainProcess(t, "MESSAGER_SYNC_BINARY", "Messager sync listening on ", "--synthetic-projection", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-none-encryption-mode=77", "--synthetic-account="+messagerAccount)
		raw, headers := process.call(t, "POST", path, `{"last_sync_timestamp":0,"missing_sequences":[9007199254740993]}`, 200, nil)
		var response messager.SyncResponse
		if err := json.Unmarshal(raw, &response); err != nil || len(response.Messages) != 1 || response.Messages[0].RoomSequence != 9007199254740993 || response.Messages[0].Content == nil || *response.Messages[0].Content != "# opaque-ciphertext" || !bytes.Contains(response.Messages[0].EncryptionMeta, []byte("9007199254740993")) {
			t.Fatal(string(raw), err)
		}
		if headers.Get("X-Total") != "1" || headers.Get("Cache-Control") != "no-store" || headers.Get("X-Solsynth-Projection") != messager.SyncProjectionVersion {
			t.Fatal(headers)
		}
		process.call(t, "GET", "/api/chat/unread", "", 503, nil) // No guessed notification enum.
		process.call(t, "POST", path, `{"last_sync_timestamp":0}`, 403, map[string]string{"Authorization": "Bearer synthetic-invalid"})
		process.stop()
	}
	other := startDomainProcess(t, "MESSAGER_SYNC_BINARY", "Messager sync listening on ", "--synthetic-projection", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-none-encryption-mode=77", "--synthetic-account="+messagerOther)
	other.call(t, "POST", path, `{"last_sync_timestamp":0}`, 403, nil)
	other.stop()
}
func TestPGTaxonomyHTTPProcessRestart(t *testing.T) {
	db := database(t)
	authority := taxonomyAuthority(0xbaff0000)
	taxonomyPublisher(t, db, authority.PublisherID)
	raw, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "synthetic-authority.json")
	if err = os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	disabled := startDomainProcess(t, "TAXONOMY_BINARY", "Taxonomy listening on ")
	disabled.call(t, "GET", "/api/posts/tags", "", 503, nil)
	disabled.stop()
	args := []string{"--synthetic-public-visibility=42", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-authority-file=" + fixture}
	const slug = "baff-process-restart"
	for i := 0; i < 2; i++ {
		process := startDomainProcess(t, "TAXONOMY_BINARY", "Taxonomy listening on ", args...)
		if i == 0 {
			process.call(t, "POST", "/api/admin/tags", fmt.Sprintf(`{"slug":%q,"owner_publisher_id":%q}`, slug, authority.PublisherID), 201, nil)
			process.call(t, "PATCH", "/api/posts/tags/"+slug+"/protect", `{"is_protected":true}`, 200, nil)
			process.call(t, "POST", "/api/posts/tags/"+slug+"/subscribe", "", 201, nil)
		}
		raw, headers := process.call(t, "GET", "/api/posts/tags/"+slug, "", 200, nil)
		var row struct {
			ID        string `json:"id"`
			Owner     string `json:"owner_publisher_id"`
			Protected bool   `json:"is_protected"`
		}
		if err := json.Unmarshal(raw, &row); err != nil || row.ID == "" || row.Owner != authority.PublisherID || !row.Protected {
			t.Fatal(string(raw), err)
		}
		if headers.Get("Cache-Control") != "no-store" || !strings.Contains(headers.Get("X-Solsynth-Compatibility"), "unverified") {
			t.Fatal(headers)
		}
		process.call(t, "GET", "/api/posts/tags/"+slug+"/subscription", "", 200, nil)
		process.call(t, "GET", "/api/posts/tags/"+slug, "", 403, map[string]string{"Cookie": "synthetic-invalid"})
		process.stop()
	}
	// Read-only authority remains useful without inventing an actor/grant.
	anonymous := startDomainProcess(t, "TAXONOMY_BINARY", "Taxonomy listening on ", "--synthetic-public-visibility=42", "--postgres-dsn-env=PG_TEST_DSN")
	anonymous.call(t, "GET", "/api/posts/tags/"+slug, "", 200, nil)
	anonymous.call(t, "POST", "/api/admin/tags", `{"slug":"must-not-exist"}`, 401, nil)
	anonymous.stop()
	var auditCount int
	if err := db.QueryRow(`SELECT count(*) FROM taxonomy_candidate.actions WHERE actor_id=$1`, authority.ActorID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatal("local audit must contain create/protect only", auditCount, err)
	}
}
