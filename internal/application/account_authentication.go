// SPDX-License-Identifier: AGPL-3.0-only
// 账号认证组装：登录签发、OAuth 授权和会话路由共享显式的权威服务。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：authenticationFor(db, db, config, verifier, guard, authorityStore, signin, apps)
package application

import (
	"database/sql"
	"errors"
	"net/http"

	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/oauthpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/authority/signin"
)

type authenticationModules struct {
	signinHTTP    http.Handler
	signinService *signin.Service
	oauthRepo     *oauthpg.Repository
	oauthHTTP     http.Handler
	sessionHTTP   http.Handler
}

func authenticationFor(
	db, authorityDB *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	aStore *authoritypg.Repository,
	signinRepo *signinpg.Repository,
	appRepo *apppg.Repository,
) (authenticationModules, error) {
	var err error
	// 只有显式登录配置才创建签发器；独立权威库不允许签发写入。
	var signinHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "SIGNIN_ISSUER_UNCONFIGURED") })
	var signinService *signin.Service
	if c.Signin != nil {
		if authorityDB != db {
			return authenticationModules{}, errors.New("sign-in requires same-pool canonical authority")
		}
		if c.SessionService == nil {
			c.SessionService, err = authority.New(aStore, c.Authority)
			if err != nil {
				return authenticationModules{}, err
			}
		}
		signinService, err = signin.New(signinRepo, c.SessionService, v, *c.Signin)
		if err != nil {
			return authenticationModules{}, err
		}
		signinHTTP = signinService.Handler()
	}
	// OAuth 复用登录签发器，并强制同库事务证明。
	var oauthRepo *oauthpg.Repository
	var oauthHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "OAUTH_UNCONFIGURED") })
	if c.OAuthEnabled {
		if authorityDB != db || guard == nil {
			return authenticationModules{}, errors.New("OAuth requires canonical same-pool authority")
		}
		if c.SessionService == nil {
			c.SessionService, err = authority.New(aStore, c.Authority)
			if err != nil {
				return authenticationModules{}, err
			}
		}
		oauthRepo, err = oauthpg.NewWithAuthority(db, aStore, c.SessionService, guard, appRepo)
		if err != nil {
			return authenticationModules{}, err
		}
		oauthHTTP = (&oauth.Service{Repository: oauthRepo, Verifier: v, Metadata: c.SessionService, Bind: guard.Bind, AuthorizationEndpoint: c.OAuthConsentURL}).Handler()
	}
	// 保留独立签发角色的响应与登录路由的覆盖优先级。
	session := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 501, "AUTHORITY_ISSUER_ROLE_REQUIRED") })
	var sessionHTTP http.Handler = session
	if c.SessionService != nil {
		sessionHTTP = c.SessionService.Handler()
		if signinService != nil {
			mux := http.NewServeMux()
			signinService.RegisterRoutes(mux)
			mux.Handle("/", sessionHTTP)
			sessionHTTP = mux
		}
	}
	return authenticationModules{
		signinHTTP:    signinHTTP,
		signinService: signinService,
		oauthRepo:     oauthRepo,
		oauthHTTP:     oauthHTTP,
		sessionHTTP:   sessionHTTP,
	}, nil
}
