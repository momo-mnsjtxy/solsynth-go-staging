// SPDX-License-Identifier: AGPL-3.0-only
// 专属入口：设备与成员先分派；签名、登录和公开目录保留自己的验证边界。
// 调用示例：if h.serveDedicatedRoutes(w, r) || h.serveProtocolRoutes(w, r) { return }。
package application

import (
	"net/http"
	"strings"

	"solsynth.local/go-migration/internal/federationhttp"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/connections"
	"solsynth.local/go-migration/ports/passport/realmboosts"
	"solsynth.local/go-migration/ports/passport/realmmembers"
)

// serveDedicatedRoutes 先处理明确独占的设备与成员入口；缺少必需模块仍返回既有 503。
// 使用示例：if h.serveDedicatedRoutes(w, r) { return }。
func (h router) serveDedicatedRoutes(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if notifications.DeviceHandles(p) {
		serveRequiredHandler(w, r, h.notificationDevices, "NOTIFICATION_UNAVAILABLE")
		return true
	}
	if activities.SensorHandles(p) {
		serveRequiredHandler(w, r, h.presenceSensors, "ACTIVITIES_UNAVAILABLE")
		return true
	}
	if connections.Handles(p) {
		serveRequiredHandler(w, r, h.accountConnections, "CONNECTION_PROVIDER_UNAVAILABLE")
		return true
	}
	if realmmembers.HandlesRequest(r) && (!realmmembers.ExperienceRoute(p) || h.realmMemberHistory) {
		serveRequiredHandler(w, r, h.realmMembers, "REALM_MEMBER_UNAVAILABLE")
		return true
	}
	if realmboosts.Handles(p) {
		serveRequiredHandler(w, r, h.realmBoosts, "REALM_BOOST_UNAVAILABLE")
		return true
	}
	if h.passkit.route(w, r) {
		return true
	}
	if handler := h.publisherCommerce.route(p); handler != nil {
		handler.ServeHTTP(w, r)
		return true
	}
	if handler := h.domainCollections.route(p); handler != nil {
		handler.ServeHTTP(w, r)
		return true
	}
	return false
}

func serveRequiredHandler(w http.ResponseWriter, r *http.Request, handler http.Handler, unavailable string) {
	if handler == nil {
		problem(w, http.StatusServiceUnavailable, unavailable)
		return
	}
	handler.ServeHTTP(w, r)
}

// serveProtocolRoutes 保留签名、登录、公开目录与存活检查各自的原始路径及认证边界。
// 使用示例：if h.serveProtocolRoutes(w, r) { return }。
func (h router) serveProtocolRoutes(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	// 联邦签名覆盖原始目标，必须在网关路径重写及 bearer 装饰器之前分派。
	if h.federationPrivate != nil && federationhttp.IsQuoteRoute(p) && r.Method != "GET" {
		h.federationPrivate.ServeHTTP(w, r)
		return true
	}
	if h.federationPublic != nil && federationhttp.IsPublicRoute(p) {
		h.federationPublic.ServeHTTP(w, r)
		return true
	}
	if h.federationPrivate != nil && federationhttp.IsPrivateRoute(p) {
		h.federationPrivate.ServeHTTP(w, r)
		return true
	}
	if h.oauth != nil && oauth.RouteMatches(p) {
		h.oauth.ServeHTTP(w, r)
		return true
	}
	if h.signin != nil && (signinRoute(r.Method, p) || signin.ContactRouteMatches(p)) {
		h.signin.ServeHTTP(w, r)
		return true
	}
	if h.realtime != nil && (p == "/ws" || under(p, "/api/realtime")) {
		h.realtime.ServeHTTP(w, r)
		return true
	}
	if h.public != nil && !h.walletExchangeMutation(r.Method, p) && publicCatalogRoute(p) {
		h.public.ServeHTTP(w, r)
		return true
	}
	if p == "/healthz" {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			problem(w, 405, "METHOD_NOT_ALLOWED")
			return true
		}
		problem(w, 200, "BACKEND_ALIVE")
		return true
	}
	return false
}

// walletExchangeMutation 让 POST 兑换进入拥有写权限的账本，而读取仍走公开配置目录。
// 使用示例：if !h.walletExchangeMutation(r.Method, p) && publicCatalogRoute(p) { ... }。
func (h router) walletExchangeMutation(method, p string) bool {
	return h.wallet != nil && method == http.MethodPost && strings.EqualFold(strings.TrimSuffix(p, "/"), "/api/wallets/exchange")
}

// signinRoute 让凭据协议自行验证因子与会话，原始 bearer 输入直接到达验证器。
// 使用示例：signinRoute("POST", "/stargate/accounts")。
func signinRoute(method, p string) bool {
	return method == "POST" && p == "/stargate/accounts" ||
		p == "/stargate/accounts/verify" ||
		p == "/stargate/accounts/verification" ||
		under(p, "/stargate/auth/challenge") ||
		under(p, "/stargate/factors") ||
		p == "/stargate/accounts/me/password" ||
		p == "/passport/accounts/recovery/password"
}
