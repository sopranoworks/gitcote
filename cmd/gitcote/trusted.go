package main

import (
	"net"
	"net/http"
	"strings"

	"github.com/sopranoworks/shoka/pkg/auth"
)

func parseCIDRs(cidrs []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, s := range cidrs {
		_, ipNet, err := net.ParseCIDR(s)
		if err != nil {
			continue
		}
		nets = append(nets, ipNet)
	}
	return nets
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP determines the real client IP. When the direct connection comes from
// a trusted proxy, the rightmost non-proxy IP in X-Forwarded-For is used.
func clientIP(r *http.Request, trustedProxies []*net.IPNet) net.IP {
	remoteIP := parseRemoteAddr(r.RemoteAddr)
	if len(trustedProxies) == 0 || !ipInNets(remoteIP, trustedProxies) {
		return remoteIP
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remoteIP
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			continue
		}
		if !ipInNets(ip, trustedProxies) {
			return ip
		}
	}
	return remoteIP
}

func parseRemoteAddr(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return net.ParseIP(addr)
	}
	return net.ParseIP(host)
}

// TrustedNetworkMiddleware checks the client IP against trustedNets. When the
// client is in a trusted network, a synthetic full-access principal is attached
// to the context and downstream auth is bypassed.
func TrustedNetworkMiddleware(trustedNets, trustedProxies []*net.IPNet, trustedPrincipal auth.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(trustedNets) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, trustedProxies)
			if ip != nil && ipInNets(ip, trustedNets) {
				ctx := auth.WithPrincipal(r.Context(), trustedPrincipal)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
