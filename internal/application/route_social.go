// SPDX-License-Identifier: AGPL-3.0-only
// 社交选路：收藏、关系、领域聊天室和公开目录保留各自前缀边界。
// 调用示例：h.selectSocialAndCatalog(&route, r.Method)，不会修改请求或规范路径。
package application

import "strings"

// selectSocialAndCatalog 覆盖社交入口；POST 兑换始终留给有写权限的钱包处理器。
// 使用示例：h.selectSocialAndCatalog(&route, r.Method)。
func (h router) selectSocialAndCatalog(route *gatewayRoute, method string) {
	service, canonical := route.service, route.canonical
	if h.interactions != nil && interactionRoute(canonical) && (service == "sphere" || service == "api") {
		route.target = h.interactions // 反应、收藏和集合覆盖通用帖子与发布者资料。
	}
	if h.social != nil && socialRoute(canonical) && (service == "passport" || service == "stargate" || service == "api") {
		route.target = h.social // 关系与领域成员保留社交授权。
	}
	if h.messager != nil && (service == "api" || service == "messager") {
		parts := strings.Split(strings.Trim(canonical, "/"), "/")
		if len(parts) == 4 && parts[0] == "api" && parts[1] == "realms" && parts[2] != "" && parts[3] == "chat" {
			route.target = h.messager // api 的领域聊天覆盖社交领域；其他服务前缀保持受限。
		}
	}
	if h.public != nil && !h.walletExchangeMutation(method, canonical) && publicCatalogRoute(canonical) && (service == "wallet" || service == "passport" || service == "api") {
		route.target = h.public // 公开目录仅承担读取及既有公开协议。
	}
}

// interactionRoute 识别反应、收藏和集合，保留发布者集合与帖子子资源的区别。
// 使用示例：interactionRoute("/api/posts/id/reactions")。
func interactionRoute(p string) bool {
	if under(p, "/api/collections") || p == "/api/posts/bookmarks" {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	return len(parts) == 4 && parts[0] == "api" && parts[1] == "posts" && (parts[3] == "reactions" ||
		parts[3] == "bookmark") ||
		len(parts) >= 4 && parts[0] == "api" && parts[1] == "publishers" && parts[3] == "collections"
}

// socialRoute 识别关系与领域入口；调用方仍必须校验服务前缀。
// 使用示例：socialRoute("/api/realms/room/members")。
func socialRoute(p string) bool {
	return under(p, "/api/relationships") || under(p, "/stargate/relationships") || under(p, "/api/realms")
}
