// SPDX-License-Identifier: AGPL-3.0-only
// 辅助业务选路：检查、翻译、补全、阅读器、日历、机器人与测验。
// 调用示例：h.selectUtilities(&route)，只覆盖规则明确允许的服务入口。
package application

import (
	"strings"

	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/passport/ipcheck"
	"solsynth.local/go-migration/ports/sphere/autocomplete"
)

// selectUtilities 把具体辅助业务交给自己的处理器，尤其区分开发者机器人与聊天室命令。
// 使用示例：h.selectUtilities(&route)。
func (h router) selectUtilities(route *gatewayRoute) {
	service, canonical := route.service, route.canonical
	if h.ipcheck != nil && (service == "api" || service == "passport") && ipcheck.Route(canonical) {
		route.target = h.ipcheck // 账户 IP 检查仅接收账户或直接 API 前缀。
	}
	if h.translation != nil && (service == "api" || service == "sphere") && canonical == "/api/translate" {
		route.target = h.translation // 内容翻译只暴露明确的单一路径。
	}
	if h.autocomplete != nil {
		if room, ok := autocomplete.Route(canonical); ok && (service == "api" || room == "" && service == "sphere" || room != "" && service == "messager") {
			route.target = h.autocomplete // 全局补全归内容，房间补全归聊天。
		}
	}
	if h.reader != nil && (service == "api" || service == "sphere") && under(canonical, "/api/scrap") {
		route.target = h.reader // 抓取阅读由阅读器自身执行目标校验。
	}
	if h.calendar != nil && (service == "api" || service == "passport") && calendarRoute(canonical) {
		route.target = h.calendar // 日历读取和纪念日由日历领域处理。
	}
	if h.bots != nil && (service == "api" ||
		service == "develop" && !bots.RoomCommandsRoute(canonical) ||
		service == "messager" && bots.RoomCommandsRoute(canonical)) && bots.RouteMatches(canonical) {
		route.target = h.bots // 开发者管理与房间命令不能借用彼此的服务前缀。
	}
	if h.tickets != nil && (service == "api" || service == "passport") && ticketRoute(canonical) {
		route.target = h.tickets // 测验、工单及管理员题库走各自权限验证。
	}
}

// ticketRoute 识别工单、测验和题库入口，具体权限由对应处理器验证。
// 使用示例：ticketRoute("/api/admin/test-questions")。
func ticketRoute(p string) bool {
	for _, base := range []string{
		"/api/tickets", "/api/tests", "/api/admin/tickets/on-call", "/api/admin/tests",
		"/api/admin/test-question-groups", "/api/admin/test-questions", "/api/admin/test-trials",
	} {
		if under(p, base) {
			return true
		}
	}
	return false
}

// calendarRoute 识别纪念日与账户日历，避免通用账户处理器吞掉日历子资源。
// 使用示例：calendarRoute("/api/accounts/me/calendar")。
func calendarRoute(p string) bool {
	if under(p, "/api/notable-days") {
		return true
	}
	v := strings.Split(strings.Trim(p, "/"), "/")
	return len(v) >= 4 && v[0] == "api" && v[1] == "accounts" && v[3] == "calendar"
}
