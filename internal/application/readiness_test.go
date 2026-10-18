// SPDX-License-Identifier: AGPL-3.0-only
// 就绪探针的回归测试：验证空后端、分库查询顺序、故障短路和 HTTP 响应。
// 运行示例：go test ./internal/application -run 'TestReady|TestReadinessHTTP'
// 下方驱动只记录 SQL，不连接 PostgreSQL；实际表、约束与数据类型另由数据库集成检查验证。
package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- 拒绝没有完成构建的后端，且不访问模块或数据库 ---
func TestReadyRejectsUnavailableBackend(t *testing.T) {
	for _, backend := range []*Backend{nil, {}, {db: &sql.DB{}}, {authorityDB: &sql.DB{}}} {
		if err := backend.Ready(context.Background()); err == nil || err.Error() != "backend unavailable" {
			t.Fatalf("expected backend unavailable, got %v", err)
		}
	}
}

// --- 保留跨数据库的检查顺序，以及登录功能的显式开关 ---
func TestReadyPreservesDatabaseProbeOrder(t *testing.T) {
	for _, signin := range []bool{false, true} {
		probe := &readinessProbe{}
		backend := readinessBackend(t, probe)
		backend.signinEnabled = signin
		if err := backend.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
		// 每项用业务字段识别查询；不复制大段 SQL，也不把它误认为数据库结构验证。
		want := []readinessCall{
			{"domain", "SELECT notify_subscribers"},
			{"authority", "('owned_authority_stream_tickets')"},
			{"authority", "authorization_epoch"},
			{"domain", "('wallet_v1.realm_grants')"},
			{"domain", "SELECT id,account_id,default_publisher_id"},
			{"domain", "owned_text_posts_realm_fk"},
			{"domain", "SELECT last_read_at,ended_by_account_id"},
			{"domain", "SELECT awarded_score,blog_metadata,pin_mode"},
			{"domain", "SELECT target_publisher_id,action_publisher_id"},
			{"domain", "owned_post_progression_source_kind"},
			{"domain", "owned_post_progression_topical_once"},
			{"domain", "SELECT credential_account_id,verified_at"},
			{"domain", "SELECT role_level,generation,permission_mask"},
			{"domain", "SELECT code_hash,auth_time,methods"},
			{"domain", "SELECT version,factor_id"},
		}
		if signin {
			want = append(want, readinessCall{"domain", "SELECT auth_methods"}, readinessCall{"domain", "('owned_authority_credentials')"})
		}
		want = append(want, readinessCall{"domain", "SELECT count(*) FROM information_schema.columns"})
		if len(probe.calls) != len(want) {
			t.Fatalf("signin=%v: got %d probes, want %d", signin, len(probe.calls), len(want))
		}
		for i, expected := range want {
			actual := probe.calls[i]
			if actual.pool != expected.pool || !strings.Contains(actual.query, expected.query) {
				t.Fatalf("probe %d: got %s %q, want %s containing %q", i+1, actual.pool, actual.query, expected.pool, expected.query)
			}
			for _, statement := range strings.Split(actual.query, ";") {
				words := strings.Fields(statement)
				if len(words) == 0 || words[0] != "SELECT" {
					t.Fatalf("probe must be read-only SELECT: %q", statement)
				}
			}
		}
	}
}

// --- 任一检查报错后，立即停止；错误同时标明业务阶段 ---
func TestReadyStopsAtEveryDatabaseFailure(t *testing.T) {
	for failAt := 1; failAt <= 18; failAt++ {
		probe := &readinessProbe{failAt: failAt}
		backend := readinessBackend(t, probe)
		backend.signinEnabled = true
		err := backend.Ready(context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), "readiness ") {
			t.Fatalf("probe %d did not report its stage: %v", failAt, err)
		}
		if len(probe.calls) != failAt {
			t.Fatalf("probe %d: later checks ran after failure: got %d calls", failAt, len(probe.calls))
		}
		if failAt == 1 && !errors.Is(err, errReadinessProbe) {
			t.Fatalf("stage annotation hid original database failure: %v", err)
		}
	}
}

// --- 缺表、无效约束和聊天缺列均不能被成功连接掩盖 ---
func TestReadyRejectsMissingSchemaEvidence(t *testing.T) {
	for _, falseAt := range []int{2, 3, 4, 6, 10, 11, 17, 18} {
		probe := &readinessProbe{falseAt: falseAt}
		backend := readinessBackend(t, probe)
		backend.signinEnabled = true
		if err := backend.Ready(context.Background()); err == nil {
			t.Fatalf("probe %d accepted missing schema evidence", falseAt)
		}
		if len(probe.calls) != falseAt {
			t.Fatalf("probe %d did not stop at missing schema evidence: %d calls", falseAt, len(probe.calls))
		}
	}
}

// --- HTTP 保留状态码与方法约束，不泄露数据库故障细节 ---
func TestReadinessHTTPContract(t *testing.T) {
	var unavailable *Backend
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodDelete} {
		response := httptest.NewRecorder()
		unavailable.readinessHTTP(response, httptest.NewRequest(method, "/readyz", nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET" {
			t.Fatalf("%s: got status=%d allow=%q", method, response.Code, response.Header().Get("Allow"))
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatalf("HEAD returned a body: %q", response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	unavailable.readinessHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "BACKEND_NOT_READY") {
		t.Fatalf("nil backend: got %d %q", response.Code, response.Body.String())
	}
	for _, failAt := range []int{0, 1} {
		backend := readinessBackend(t, &readinessProbe{failAt: failAt})
		response := httptest.NewRecorder()
		backend.readinessHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		want := http.StatusOK
		if failAt > 0 {
			want = http.StatusServiceUnavailable
		}
		if response.Code != want || strings.Contains(response.Body.String(), errReadinessProbe.Error()) {
			t.Fatalf("probe %d: got %d %q", failAt, response.Code, response.Body.String())
		}
	}
}

// --- 测试专用数据库：记录池与查询，只返回就绪检查所需的标量 ---
var errReadinessProbe = errors.New("synthetic readiness database failure")

type readinessCall struct{ pool, query string }
type readinessProbe struct {
	calls           []readinessCall
	failAt, falseAt int
}
type readinessConnector struct {
	probe *readinessProbe
	pool  string
}
type readinessDriver struct{}
type readinessConn struct{ readinessConnector }
type readinessRows struct {
	value driver.Value
	done  bool
}

func readinessBackend(t *testing.T, probe *readinessProbe) *Backend {
	t.Helper()
	domain := sql.OpenDB(readinessConnector{probe: probe, pool: "domain"})
	authority := sql.OpenDB(readinessConnector{probe: probe, pool: "authority"})
	t.Cleanup(func() { _ = domain.Close(); _ = authority.Close() })
	return &Backend{db: domain, authorityDB: authority}
}
func (c readinessConnector) Connect(context.Context) (driver.Conn, error) {
	return &readinessConn{c}, nil
}
func (readinessConnector) Driver() driver.Driver { return readinessDriver{} }
func (readinessDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the readiness test connector")
}
func (*readinessConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared readiness query")
}
func (*readinessConn) Begin() (driver.Tx, error) {
	return nil, errors.New("readiness must not open a transaction")
}
func (*readinessConn) Close() error { return nil }
func (c *readinessConn) record(query string) error {
	c.probe.calls = append(c.probe.calls, readinessCall{c.pool, query})
	if len(c.probe.calls) == c.probe.failAt {
		return errReadinessProbe
	}
	return nil
}
func (c *readinessConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if err := c.record(query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(0), nil
}
func (c *readinessConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := c.record(query); err != nil {
		return nil, err
	}
	var value driver.Value = len(c.probe.calls) != c.probe.falseAt
	if strings.Contains(query, "SELECT count(*) FROM information_schema.columns") {
		value = int64(4)
		if len(c.probe.calls) == c.probe.falseAt {
			value = int64(3)
		}
	}
	return &readinessRows{value: value}, nil
}
func (*readinessRows) Columns() []string { return []string{"ready"} }
func (*readinessRows) Close() error      { return nil }
func (r *readinessRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	values[0], r.done = r.value, true
	return nil
}
