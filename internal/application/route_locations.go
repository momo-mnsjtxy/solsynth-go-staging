// SPDX-License-Identifier: AGPL-3.0-only
// 地理与运势选路：把显式历史前缀转换成领域读取的路径，不改调用方请求。
// 调用示例：if h.serveLocationAliases(w, r) || h.serveFortuneAliases(w, r) { return }。
package application

import "net/http"

// serveLocationAliases 接受账户和内容服务共享的地理入口，只规范化领域路径。
// 使用示例：if h.serveLocationAliases(w, r) { return }。
func (h router) serveLocationAliases(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if h.locations != nil {
		canonical := aliasPath(p, "/passport/", "/sphere/")
		if under(canonical, "/api/meets") ||
			under(canonical, "/api/pins") ||
			under(canonical, "/api/nfc") ||
			under(canonical, "/api/nearby") ||
			under(canonical, "/api/admin/nfc") ||
			under(canonical, "/api/locations") {
			copy := requestAtPath(r, canonical)
			h.locations.ServeHTTP(w, copy)
			return true
		}
	}
	return false
}

// serveFortuneAliases 规范化旧账户前缀，保留两个明确的运势入口。
// 使用示例：if h.serveFortuneAliases(w, r) { return }。
func (h router) serveFortuneAliases(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if h.fortunes != nil {
		canonical := aliasPath(p, "/passport/")
		if canonical == "/api/fortune" || canonical == "/api/fortune/random" {
			copy := requestAtPath(r, canonical)
			h.fortunes.ServeHTTP(w, copy)
			return true
		}
	}
	return false
}
