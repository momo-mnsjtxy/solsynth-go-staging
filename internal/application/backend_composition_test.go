// SPDX-License-Identifier: AGPL-3.0-only
// 应用组装回归：验证构造无数据库访问、可选能力默认关闭，以及跨库配置不能取得原子写入能力。
// 调用：go test ./internal/application -run 'TestPostgresComposition'。
// 示例：新增一个可选业务时，将它的默认不可用入口加入默认能力测试。
package application

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/blogpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/internal/translationpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

// 拒绝建立任何连接，确保组装过程中没有隐藏的查询、迁移或后台数据库任务。
type compositionConnector struct{ connections atomic.Int32 }

func (c *compositionConnector) Connect(context.Context) (driver.Conn, error) {
	c.connections.Add(1)
	return nil, errors.New("database access during application assembly")
}
func (*compositionConnector) Driver() driver.Driver { return compositionDriver{} }

type compositionDriver struct{}

func (compositionDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("database access during application assembly")
}

func compositionDatabase(t *testing.T) (*sql.DB, *compositionConnector) {
	t.Helper()
	connector := &compositionConnector{}
	db := sql.OpenDB(connector)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if n := connector.connections.Load(); n != 0 {
			t.Errorf("application assembly or disabled capability opened %d database connections", n)
		}
	})
	return db, connector
}

func compositionConfig(t *testing.T) Config {
	t.Helper()
	// 复用已有测试的临时密钥，不引入真实数据库或生产凭据。
	hostIdentity(t)
	visibility := int32(0)
	return Config{
		PublicVisibility: &visibility,
		MessagerPolicy:   &messagerpg.IndependentPolicy{NoneEncryptionMode: 0, NotifyAll: 0, NotifyMentions: 1, NotifyNone: 2},
		Authority: authority.Config{
			Issuer: "https://composition-test.invalid", Audience: "application-assembly",
			TrustedKeys: map[string]*rsa.PublicKey{"ephemeral": &hostKey.PublicKey},
		},
	}
}

func TestPostgresCompositionDoesNotConnectDuringAssembly(t *testing.T) {
	for _, independent := range []bool{false, true} {
		name := "same_pool"
		if independent {
			name = "explicit_independent_authority"
		}
		t.Run(name, func(t *testing.T) {
			db, _ := compositionDatabase(t)
			var authorityDB *sql.DB
			c := compositionConfig(t)
			if independent {
				authorityDB, _ = compositionDatabase(t)
				c.AllowNonAtomicAuthorityWrites = true
			}
			backend, err := NewPostgres(db, authorityDB, c)
			if err != nil {
				t.Fatal(err)
			}
			if backend.db != db || backend.handler == nil || backend.verifier == nil || backend.wallet == nil || backend.chat == nil || backend.postComposition == nil || backend.progression == nil || backend.activities == nil {
				t.Fatal("required business dependencies were not assembled")
			}
			if authorityDB == nil {
				authorityDB = db
			}
			if backend.authorityDB != authorityDB {
				t.Fatal("canonical authority pool changed during assembly")
			}
			if independent {
				if backend.tickets != nil || backend.affiliations != nil || backend.bots != nil || backend.scheduledPosts != nil || backend.accountSettings != nil {
					t.Fatal("independent authority acquired same-pool-only business capabilities")
				}
			} else if backend.tickets == nil || backend.affiliations == nil || backend.bots == nil || backend.scheduledPosts == nil || backend.accountSettings == nil {
				t.Fatal("same-pool business dependency was lost")
			}
			if backend.signinEnabled || backend.voiceEnabled || backend.reader != nil || backend.translation != nil || backend.oauth != nil || backend.media != nil || backend.fortunes != nil || backend.blogs != nil {
				t.Fatal("optional capability enabled without explicit configuration")
			}
			if backend.moderationApp != "dev.solsynth.solian" || backend.notificationWorker == nil || !backend.notificationWorker.Chat || !backend.notificationWorker.Wallet || len(backend.notificationWorker.LocalProjections) != 3 {
				t.Fatal("default notification namespace or durable projections changed")
			}
		})
	}
}

func TestPostgresCompositionOptionalRoutesStayExplicitlyDisabled(t *testing.T) {
	db, _ := compositionDatabase(t)
	backend, err := NewPostgres(db, nil, compositionConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, code string
		status             int
	}{
		{"GET", "/api/scrap/link", "READER_UNCONFIGURED", 503},
		{"POST", "/api/translate", "TRANSLATION_UNCONFIGURED", 503},
		{"GET", "/api/fortune", "FORTUNE_UNCONFIGURED", 503},
		{"GET", "/api/media/files", "MEDIA_STORAGE_UNCONFIGURED", 503},
		{"GET", "/api/presence/artworks", "MEDIA_STORAGE_UNCONFIGURED", 503},
		{"POST", "/stargate/auth/challenge", "SIGNIN_ISSUER_UNCONFIGURED", 503},
		{"POST", "/auth/open/token", "OAUTH_UNCONFIGURED", 503},
		{"POST", "/stargate/auth/token", "AUTHORITY_ISSUER_ROLE_REQUIRED", 501},
	} {
		t.Run(tc.code+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			backend.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) {
				t.Fatalf("%s %s: got %d %s", tc.method, tc.path, response.Code, response.Body.String())
			}
		})
	}
}

func TestPostgresCompositionRejectsCrossPoolCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		configure     func(*Config)
	}{
		{"reader", "reader requires canonical same-pool authority", func(c *Config) { c.Reader = &readerpg.Config{} }},
		{"translation", "translation requires canonical same-pool authority", func(c *Config) { c.Translation = &translationpg.Config{} }},
		{"blogs", "blogs require canonical same-pool authority", func(c *Config) { c.Blogs = &blogpg.Config{} }},
		{"signin", "sign-in requires same-pool canonical authority", func(c *Config) { c.Signin = &signin.Config{} }},
		{"oauth", "OAuth requires canonical same-pool authority", func(c *Config) { c.OAuthEnabled = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := compositionDatabase(t)
			authorityDB, _ := compositionDatabase(t)
			c := compositionConfig(t)
			c.AllowNonAtomicAuthorityWrites = true
			tc.configure(&c)
			backend, err := NewPostgres(db, authorityDB, c)
			if backend != nil || err == nil || err.Error() != tc.message {
				t.Fatalf("got backend=%v err=%v, want %q", backend != nil, err, tc.message)
			}
		})
	}
}

func TestPostgresCompositionKeepsVoiceAndMLSPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		configure     func(*Config)
	}{
		{"voice_without_storage", "voice requires canonical media and explicit retention from one hour to one year", func(c *Config) { c.ChatVoiceRetention = time.Hour }},
		{"ambiguous_mls_mode", "plaintext mapping must differ from authored MLS mode 3", func(c *Config) { c.EnableMLSDelivery = true; c.MessagerPolicy.NoneEncryptionMode = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := compositionDatabase(t)
			c := compositionConfig(t)
			tc.configure(&c)
			backend, err := NewPostgres(db, nil, c)
			if backend != nil || err == nil || err.Error() != tc.message {
				t.Fatalf("got backend=%v err=%v, want %q", backend != nil, err, tc.message)
			}
		})
	}
}
