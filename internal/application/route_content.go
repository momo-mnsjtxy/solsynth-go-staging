// SPDX-License-Identifier: AGPL-3.0-only
// 内容业务选路：发布账户、媒体、审核、时间线与通话的处理器所有权。
// 调用示例：h.selectPublishingAndMedia(&route)；调用顺序集中写在 selectGatewayRoute。
package application

import (
	"net/http"
	"strings"

	"solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/sphere/blogshttp"
	"solsynth.local/go-migration/ports/sphere/livestreams"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

// selectPublishingAndMedia 选择发布资料、博客和文件的专属领域，不让默认账户/帖子处理器吞掉入口。
// 使用示例：h.selectPublishingAndMedia(&route)。
func (h router) selectPublishingAndMedia(route *gatewayRoute) {
	service, canonical := route.service, route.canonical
	if h.accounts != nil && (canonical == "/api/account/publishing" ||
		under(canonical, "/api/publishers") ||
		under(canonical, "/api/accounts") && !boardRoute(canonical) && canonical != "/api/accounts/me/pin-status") && (service == "sphere" ||
		service == "passport" ||
		service == "api") {
		route.target = h.accounts // 账户与发布者资料不交给帖子或看板。
	}
	if h.blogs != nil && (service == "sphere" || service == "api") && blogshttp.Handles(canonical) {
		route.target = h.blogs // 博客覆盖一般发布者资料入口。
	}
	if h.notifications != nil && (under(canonical, "/api/notifications") || under(canonical, "/metoer/notifications")) && (service == "api" || service == "metoer") {
		route.target = h.notifications // 保留既有 metoer 通知前缀拼写，避免破坏调用方。
	}
	if h.media != nil && (under(canonical, "/api/media") ||
		under(canonical, "/api/files") ||
		under(canonical, "/api/pools") ||
		under(canonical, "/api/bundles") ||
		under(canonical, "/api/billing")) && (service == "drive" ||
		service == "api") {
		route.target = h.media // 上传、文件池与媒体账单由媒体领域处理。
	}
}

// selectContentManagement 先选择审核与直播，再让管理员扩展覆盖其明确拥有的入口。
// 使用示例：h.selectContentManagement(&route)。
func (h router) selectContentManagement(route *gatewayRoute) {
	service, canonical := route.service, route.canonical
	if h.moderation != nil && content.Handles(canonical) && (service == "api" ||
		service == "passport" && under(canonical, "/api/admin/realms") ||
		service == "sphere" && !under(canonical, "/api/admin/realms")) {
		route.target = h.moderation // 领域审核仅接受与其管理对象对应的前缀。
	}
	if h.livestreams != nil && (service == "api" || service == "sphere") && livestreams.RouteMatches(canonical) {
		route.target = h.livestreams // 直播的会话和令牌由直播领域验证。
	}
	if admin := h.admin.route(service, canonical); admin != nil {
		route.target = admin // 明确的管理员扩展优先于通用审核领域。
	}
}

// selectContentAndCalls 以请求方法区分时间线读取，通话专属入口最后覆盖通用聊天室。
// 使用示例：h.selectContentAndCalls(&route, r)。
func (h router) selectContentAndCalls(route *gatewayRoute, r *http.Request) {
	service, canonical := route.service, route.canonical
	if service == "sphere" || service == "api" {
		if h.surveys != nil && under(canonical, "/api/surveys") {
			route.target = h.surveys // 投票入口归问卷领域。
		}
		if h.stickers != nil && under(canonical, "/api/stickers") {
			route.target = h.stickers // 表情包入口归贴纸领域。
		}
	}
	if h.timeline != nil && (service == "sphere" || service == "api") {
		probe := requestAtPath(r, canonical) // 用副本判定方法与路径，避免改动入口日志目标。
		if timeline.Handles(probe) {
			route.target = h.timeline // 列表读取和回复链覆盖普通帖子，写帖仍归帖子领域。
		}
	}
	if h.calls != nil && (service == "api" || service == "messager") && under(canonical, "/api/chat/realtime") {
		route.target = h.calls // 通话加入和令牌入口不能被普通聊天室吞掉。
	}
}

// boardRoute 识别账户与应用看板；不能把全部账户路径当成看板。
// 使用示例：boardRoute("/api/accounts/me/board")。
func boardRoute(p string) bool {
	return (under(p, "/api/accounts") ||
		under(p, "/api/admin/accounts")) && (strings.HasSuffix(p, "/board") ||
		strings.Contains(p, "/board/items/")) ||
		under(p, "/api/private/apps") && strings.Contains(p, "/board/")
}

// taxonomyRoute 把标签、分类与订阅从通用帖子领域中分离。
// 使用示例：taxonomyRoute("/api/posts/tags")。
func taxonomyRoute(p string) bool {
	for _, base := range []string{"/api/posts/tags", "/api/posts/categories", "/api/admin/posts/tags", "/api/admin/tags", "/api/admin/categories", "/api/categories/subscriptions"} {
		if under(p, base) {
			return true
		}
	}
	return false
}
