package v0

import (
	"net"
	"net/http"
	"strings"
)

// extractClientIP trusts Caddy's appended X-Forwarded-For only when the direct peer is local.
func extractClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			forwarded := strings.TrimSpace(parts[i])
			if net.ParseIP(forwarded) != nil {
				return forwarded
			}
		}
	}

	return host
}
