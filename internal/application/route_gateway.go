// SPDX-License-Identifier: AGPL-3.0-only
// 业务网关：明确前缀的所有权，再按既有覆盖顺序选择领域处理器。
// 调用示例：route := h.selectGatewayRoute(httptest.NewRequest("GET", "/sphere/posts", nil))。
package application

import (
	"net/http"
	"strings"
)

// gatewayRoute 保存一次请求的选路数据，业务族只修改 target；canonical 由入口唯一确定。
// 使用示例：route := h.gatewayEntry("/messager/chat/room/sync")。
type gatewayRoute struct {
	service                string
	canonical              string
	target                 http.Handler
	appIdentityUnsupported bool
}

// selectGatewayRoute 按顺序叠加具体业务所有权；后面的专属业务覆盖前面的通用领域。
// 使用示例：route := h.selectGatewayRoute(r)，随后把 requestAtPath(r, route.canonical) 交给 route.target。
func (h router) selectGatewayRoute(r *http.Request) gatewayRoute {
	route := h.gatewayEntry(r.URL.Path)        // 服务前缀只授予对应领域的基础入口。
	h.selectPublishingAndMedia(&route)         // 账户发布、博客、通知和文件覆盖基础入口。
	h.selectSocialAndCatalog(&route, r.Method) // 关系、领域聊天室和公开读取各有独立处理器。
	h.selectAccountTools(&route)               // 设置、改名卡和年度回顾保留账户边界。
	h.selectContentManagement(&route)          // 审核、直播与管理员扩展覆盖各自领域。
	h.selectUtilities(&route)                  // 搜索、日历、机器人和测验只接收规定前缀。
	h.selectCommerceAndApps(&route)            // 账单归属和应用身份校验先于最终分派确定。
	h.selectContentAndCalls(&route, r)         // 时间线和通话覆盖帖子与聊天室的通用读取。
	return route
}

// gatewayEntry 解析受支持的服务前缀，确定领域所见的规范路径和基础处理器。
// 使用示例：h.gatewayEntry("/api/auth/token") 的 canonical 为 "/stargate/auth/token"。
func (h router) gatewayEntry(p string) gatewayRoute {
	service, rest, found := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	route := gatewayRoute{service: service, canonical: p}
	switch service {
	case "passport", "develop", "sphere", "messager", "drive", "wallet":
		route.canonical = "/api" // 裸服务前缀仍交给原来的领域根入口。
		if found {
			route.canonical += "/" + rest
		}
		switch service {
		case "passport", "develop":
			if boardRoute(route.canonical) && (service != "develop" || under(route.canonical, "/api/private/apps")) {
				route.target = h.board
			}
		case "sphere":
			if taxonomyRoute(route.canonical) {
				route.target = h.taxonomy
			} else if under(route.canonical, "/api/posts") {
				route.target = h.posts
			}
		case "messager":
			route.target = h.messager
		case "drive":
			route.target = h.media
		case "wallet":
			route.target = h.wallet
		}
	case "e2ee":
		if under(p, "/e2ee/mls") {
			route.target = h.messager
		}
	case "stargate":
		h.selectStargateEntry(&route)
	case "api":
		h.selectAPIEntry(&route)
	}
	return route
}

func (h router) selectStargateEntry(route *gatewayRoute) {
	p := route.canonical
	switch {
	case under(p, "/stargate/e2ee/mls"):
		route.target = h.messager
		route.canonical = strings.TrimPrefix(p, "/stargate")
	case p == "/stargate/accounts/me/pin-status":
		route.target = h.wallet
		route.canonical = "/api/accounts/me/pin-status"
	case under(p, "/stargate/accounts"):
		route.target = h.accounts
	default:
		route.target = h.session // 会话协议原样保留 stargate 路径。
	}
}

func (h router) selectAPIEntry(route *gatewayRoute) {
	p := route.canonical
	switch {
	case under(p, "/api/wallets"), p == "/api/accounts/me/pin-status":
		route.target = h.wallet
	case under(p, "/api/e2ee/mls"):
		route.target = h.messager
		route.canonical = strings.TrimPrefix(p, "/api")
	case under(p, "/api/chat"):
		route.target = h.messager
	case taxonomyRoute(p):
		route.target = h.taxonomy
	case under(p, "/api/posts"):
		route.target = h.posts
	case boardRoute(p):
		route.target = h.board
	case under(p, "/api/accounts"), under(p, "/api/publishers"):
		route.target = h.accounts
	case under(p, "/api/sessions"), under(p, "/api/auth"):
		route.target = h.session
		route.canonical = "/stargate/" + strings.TrimPrefix(p, "/api/")
	}
}
