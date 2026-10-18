// SPDX-License-Identifier: AGPL-3.0-only
// HTTP 入口：校验请求路径，按业务族选择处理器，并保留调用方的响应形式。
// 调用示例：router{modules{posts: postHandler}}.ServeHTTP(w, r)。
package application

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// modules 按业务主体列出进程内处理器；路由只选择已装配的处理器，不改变其认证边界。
// 使用示例：router{modules{posts: postHandler, messager: chatHandler}}。
type modules struct {
	// 账户、关系与个人资料。
	accounts, session, signin, oauth                            http.Handler
	accountConnections, accountHistory, accountSettings         http.Handler
	friends, activities, presenceSensors, presence, progression http.Handler
	affiliations, artwork, namecards, rewind, ipcheck, fortunes http.Handler
	realmMembers, realmBoosts, social                           http.Handler
	realmMemberHistory                                          bool

	// 内容、聊天与媒体。
	board, taxonomy, posts, blogs, interactions, timeline http.Handler
	messager, calls, realtime, media, locations           http.Handler
	notifications, notificationDevices                    http.Handler
	livestreams, moderation, surveys, stickers            http.Handler
	translation, autocomplete, reader, calendar           http.Handler

	// 钱包、开发者与联邦协议。
	wallet, public, commerce, merchants, tickets, apps, bots http.Handler
	federationPublic, federationPrivate                      http.Handler
	passkit                                                  passkitModules
	publisherCommerce                                        publisherCommerceModules
	domainCollections                                        domainCollectionModules
	admin                                                    adminModules
}

type router struct{ modules }

// under 判断完整路径段边界，避免 /api/accounts-other 被当成 /api/accounts。
// 使用示例：under("/api/accounts/me", "/api/accounts")。
func under(p, base string) bool { return p == base || strings.HasPrefix(p, base+"/") }

// ServeHTTP 依次处理入口校验、专属入口、历史别名和业务网关，再反馈统一路由结果。
// 使用示例：httptest.NewServer(router{modules{posts: postHandler}})。
func (h router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w = headWriter{w}
	}
	if !validRoutePath(r.URL) {
		problem(w, http.StatusBadRequest, "INVALID_ROUTE_PATH")
		return
	}
	// 专属入口必须先于网关选路，保留设备、签名和凭据协议自己的验证流程。
	if h.serveDedicatedRoutes(w, r) || h.serveProtocolRoutes(w, r) ||
		h.serveAccountAliases(w, r) || h.serveAccountExtras(w, r) ||
		h.serveLocationAliases(w, r) || h.serveFortuneAliases(w, r) {
		return
	}
	route := h.selectGatewayRoute(r)
	if route.appIdentityUnsupported {
		problem(w, http.StatusNotImplemented, "APP_IDENTITY_UNSUPPORTED")
		return
	}
	if route.target == nil {
		http.NotFound(w, r)
		return
	}
	copy := requestAtPath(r, route.canonical)
	copy.URL.RawPath = ""
	// 领域处理器读取 URL；入口日志继续读取调用方原始 RequestURI。
	route.target.ServeHTTP(locationWriter{ResponseWriter: w, canonicalPrefix: "/api", externalPrefix: externalPrefix(route.service)}, copy)
}

func validRoutePath(u *url.URL) bool {
	p := u.Path
	return p != "" && p[0] == '/' && path.Clean(p) == p &&
		!strings.Contains(p, "\\") && !strings.Contains(p, "//") &&
		(u.RawPath == "" || u.EscapedPath() == p)
}

// requestAtPath 只复制请求及 URL 后替换路径，保留查询、上下文、签名与原始 RequestURI。
// 使用示例：canonicalRequest := requestAtPath(r, "/api/accounts/me")。
func requestAtPath(r *http.Request, canonical string) *http.Request {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Path = canonical
	copy.URL = &u
	return copy
}

// aliasPath 只接受显式列出的旧服务前缀；不把任意前缀当成领域访问权限。
// 使用示例：aliasPath("/passport/activities", "/passport/", "/stargate/") == "/api/activities"。
func aliasPath(p string, prefixes ...string) string {
	for _, prefix := range prefixes {
		if strings.HasPrefix(p, prefix) {
			return "/api/" + strings.TrimPrefix(p, prefix)
		}
	}
	return p
}

// externalPrefix 只对既有网关别名重写跳转，drive 与其他入口继续返回 /api。
// 使用示例：externalPrefix("sphere") == "/sphere"。
func externalPrefix(service string) string {
	switch service {
	case "passport", "develop", "sphere", "messager", "wallet":
		return "/" + service
	}
	return "/api"
}

// locationWriter 将领域返回的站内跳转还原为调用方使用的服务前缀；外部地址原样返回。
// 使用示例：locationWriter{ResponseWriter: w, canonicalPrefix: "/api", externalPrefix: "/sphere"}。
type locationWriter struct {
	http.ResponseWriter
	canonicalPrefix, externalPrefix string
}

func (w locationWriter) WriteHeader(status int) {
	if raw := w.Header().Get("Location"); raw != "" && w.externalPrefix != w.canonicalPrefix {
		if u, err := url.Parse(raw); err == nil && !u.IsAbs() && u.Host == "" && under(u.Path, w.canonicalPrefix) {
			u.Path = w.externalPrefix + strings.TrimPrefix(u.Path, w.canonicalPrefix)
			u.RawPath = ""
			w.Header().Set("Location", u.String())
		}
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w locationWriter) Write(b []byte) (int, error) { return w.ResponseWriter.Write(b) }

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(b []byte) (int, error) { return len(b), nil }
