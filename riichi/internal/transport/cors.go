package transport

import (
	"net/http"
	"strings"
)

// withCORS 给 HTTP handler 加最小 CORS 支持。
//
// 为什么需要：前端演示页可能是 file:// 打开（Origin 为 "null"）或跑在另一个端口，
// 浏览器会同源策略拦掉 fetch（尤其是带 Authorization 的预检请求），
// 导致「拿不到自己的手牌 → 点不了牌」。WebSocket 不受同源限制，所以只有 HTTP 需要它。
//
// 安全边界：origin 由调用方（-cors flag / Config.CORSOrigin）决定，默认 "*" 只适合 demo；
// 生产必须收窄成白名单域名，并且只放开确实需要的 header。
func withCORS(next http.Handler, origin string) http.Handler {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			// 预检请求：只回 CORS 头，不进业务 handler。
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
