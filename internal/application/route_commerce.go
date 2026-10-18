// SPDX-License-Identifier: AGPL-3.0-only
// 钱包与应用选路：账单归商户、订单归交易，应用入口保持身份约束。
// 调用示例：h.selectCommerceAndApps(&route)，入口随后反馈 appIdentityUnsupported。
package application

// selectCommerceAndApps 保留账单与订单边界；没有应用处理器或使用错误前缀时仍拒绝应用身份。
// 使用示例：h.selectCommerceAndApps(&route)。
func (h router) selectCommerceAndApps(route *gatewayRoute) {
	service, canonical := route.service, route.canonical
	if h.merchants != nil && (service == "api" ||
		service == "wallet") && (under(canonical, "/api/merchants") ||
		under(canonical, "/api/admin/payments") ||
		under(canonical, "/api/admin/subscriptions") ||
		under(canonical, "/api/admin/wallet-products") ||
		canonical == "/api/subscriptions/billing") {
		route.target = h.merchants // 订阅结算归商户，不能落入普通订阅 CRUD。
	}
	if h.commerce != nil && canonical != "/api/subscriptions/billing" && (under(canonical, "/api/orders") ||
		under(canonical, "/api/subscriptions") ||
		under(canonical, "/api/coupons")) && (service == "wallet" ||
		service == "api") {
		route.target = h.commerce // 订单、优惠券和非结算订阅归交易。
	}
	if h.apps != nil && appRoute(canonical) && (service == "develop" || service == "api") {
		route.target = h.apps // 应用入口覆盖应用看板的基础目标。
	}
	if under(canonical, "/api/private/apps") && (h.apps == nil || (service != "develop" && service != "api")) {
		route.appIdentityUnsupported = true // 缺少处理器或借错服务前缀时，入口反馈既有 501。
	}
}

// appRoute 只判断应用领域路径，服务身份限制由 selectCommerceAndApps 统一决定。
// 使用示例：appRoute("/api/private/apps/app")。
func appRoute(p string) bool {
	return p == "/api/develop/quota" || under(p, "/api/developers") || under(p, "/api/private/projects") || under(p, "/api/private/apps") || under(p, "/api/apps")
}
