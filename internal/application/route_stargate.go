// SPDX-License-Identifier: AGPL-3.0-only
// Stargate 业务入口：上游将账户和钱包服务搬到 /stargate，仍由现有业务验证权限。
// 调用示例：movedStargatePath("/stargate/wallets/me") 返回 "/api/wallets/me"。
package application

import "strings"

// movedStargatePath 只转换已经迁入 Stargate 的业务。登录、因子、会话、OAuth
// 和签名协议保留自己的原始路径，不能用任意 /stargate 前缀访问其他领域。
func movedStargatePath(p string) (string, bool) {
	if !strings.HasPrefix(p, "/stargate/") {
		return "", false
	}
	canonical := "/api/" + strings.TrimPrefix(p, "/stargate/")
	if under(canonical, "/api/accounts") {
		// 注册、账户读取和密码协议已经有原生 Stargate 入口；只接管搬迁的子资源。
		if len(strings.Split(strings.Trim(canonical, "/"), "/")) < 4 ||
			canonical == "/api/accounts/me/password" ||
			under(canonical, "/api/accounts/me/name-change-card") {
			return "", false
		}
		// 激活测验仍属于 Passport，不能随账户资料一起搬迁。
		if under(canonical, "/api/accounts/me/activation") {
			return "", false
		}
		return canonical, true
	}
	for _, base := range []string{
		"/api/relationships", "/api/friends", "/api/activities", "/api/presence/artworks",
		"/api/realms", "/api/affiliations", "/api/notable-days",
		"/api/nfc", "/api/nearby", "/api/pins", "/api/locations", "/api/domain-blocks",
		"/api/ip-check", "/api/fortune", "/api/badges", "/api/notifications",
		"/api/wallets", "/api/orders", "/api/subscriptions", "/api/coupons",
		"/api/merchants", "/api/wallet-products",
		"/api/admin/accounts", "/api/admin/realms", "/api/admin/nfc",
		"/api/admin/progression", "/api/admin/payments", "/api/admin/subscriptions",
		"/api/admin/wallet-products",
	} {
		if under(canonical, base) {
			return canonical, true
		}
	}
	return "", false
}
