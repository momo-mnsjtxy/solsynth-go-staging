// SPDX-License-Identifier: AGPL-3.0-only
// 账户选路：个人历史、活动、状态、附属资料和账户工具的旧前缀与优先级。
// 调用示例：if h.serveAccountAliases(w, r) { return }；网关内使用 h.selectAccountTools(&route)。
package application

import (
	"net/http"
	"strings"

	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accountsettings"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/connections"
	"solsynth.local/go-migration/ports/passport/friends"
	"solsynth.local/go-migration/ports/passport/history"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/progression"
	"solsynth.local/go-migration/ports/passport/rewind"
)

// serveAccountAliases 把显式账户别名交给领域处理器；原请求和原始跳转头保持不变。
// 使用示例：if h.serveAccountAliases(w, r) { return }。
func (h router) serveAccountAliases(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	canonical := aliasPath(p, "/passport/", "/stargate/")
	var handler http.Handler
	if history.Handles(canonical) {
		if h.accountHistory == nil {
			problem(w, 503, "ACCOUNT_HISTORY_UNAVAILABLE")
			return true
		}
		handler = h.accountHistory // 历史读接口缺少模块时明确失败，不退回普通账户接口。
	}
	if friends.Handles(p) {
		handler = h.friends // 好友概览保留既有的原路径匹配规则。
	}
	if activities.Handles(canonical) {
		handler = h.activities // 活动列表交给活动领域。
	}
	if presence.RouteMatches(canonical) {
		handler = h.presence // 在线状态拥有独立授权与可见性规则。
	}
	if connections.Handles(canonical) {
		handler = h.accountConnections // 连接提供方覆盖通用账户入口。
	}
	if activities.SensorHandles(canonical) {
		handler = h.presenceSensors // 传感器覆盖通用在线状态处理器；nil 时保留旧回退行为。
	}
	if notifications.DeviceHandles(canonical) {
		handler = h.notificationDevices // 通知设备由同意和验证流程管理。
	}
	if progression.RouteMatches(canonical) {
		handler = h.progression // 成长进度覆盖匹配到的账户读入口。
	}
	if handler != nil {
		copy := requestAtPath(r, canonical)
		handler.ServeHTTP(w, copy)
		return true
	}
	return false
}

// serveAccountExtras 保留账户附属资料、激活测验和状态原画的独占选路顺序。
// 使用示例：if h.serveAccountExtras(w, r) { return }。
func (h router) serveAccountExtras(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if h.affiliations != nil && (under(p, "/api/affiliations") || under(p, "/passport/affiliations")) {
		copy := requestAtPath(r, strings.Replace(p, "/passport/", "/api/", 1))
		h.affiliations.ServeHTTP(w, copy)
		return true
	}
	if h.tickets != nil && (p == "/api/accounts/me/activation/progress" || p == "/passport/accounts/me/activation/progress") {
		copy := requestAtPath(r, "/api/tests/activation")
		h.tickets.ServeHTTP(w, copy)
		return true
	}
	if h.artwork != nil && under(p, "/api/presence/artworks") {
		h.artwork.ServeHTTP(w, r)
		return true
	}
	return false
}

// selectAccountTools 让设置、改名卡与回顾使用自己的验证和数据访问边界。
// 使用示例：h.selectAccountTools(&route)。
func (h router) selectAccountTools(route *gatewayRoute) {
	service, canonical := route.service, route.canonical
	if h.accountSettings != nil && (service == "api" || service == "passport") && accountsettings.RouteMatches(canonical) {
		route.target = h.accountSettings // 设置保留自己的敏感操作验证。
	}
	if service == "api" || service == "passport" || service == "stargate" {
		if h.namecards != nil && namecards.RouteMatches(canonical) {
			route.target = h.namecards // 改名卡订单和使用不进入普通账户修改。
		}
		if h.rewind != nil && rewind.RouteMatches(canonical) {
			route.target = h.rewind // 回顾处理器负责年度与公开分享边界。
		}
	}
}
