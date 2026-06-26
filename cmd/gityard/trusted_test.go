package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sopranoworks/shoka/pkg/auth"
)

func TestParseCIDRs(t *testing.T) {
	nets := parseCIDRs([]string{"127.0.0.0/8", "192.168.0.0/16", "invalid"})
	if len(nets) != 2 {
		t.Fatalf("got %d nets, want 2", len(nets))
	}
}

func TestIpInNets(t *testing.T) {
	nets := parseCIDRs([]string{"127.0.0.0/8", "192.168.0.0/16"})

	if !ipInNets(net.ParseIP("127.0.0.1"), nets) {
		t.Error("127.0.0.1 should be in 127.0.0.0/8")
	}
	if !ipInNets(net.ParseIP("192.168.1.5"), nets) {
		t.Error("192.168.1.5 should be in 192.168.0.0/16")
	}
	if ipInNets(net.ParseIP("8.8.8.8"), nets) {
		t.Error("8.8.8.8 should NOT be in trusted nets")
	}
}

func TestClientIP_Direct(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.5:12345"

	ip := clientIP(r, nil)
	if ip.String() != "192.168.1.5" {
		t.Errorf("got %v, want 192.168.1.5", ip)
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	proxies := parseCIDRs([]string{"172.16.0.0/12"})

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.16.0.1:12345"
	r.Header.Set("X-Forwarded-For", "192.168.1.5, 172.16.0.2")

	ip := clientIP(r, proxies)
	if ip.String() != "192.168.1.5" {
		t.Errorf("got %v, want 192.168.1.5 (rightmost non-proxy)", ip)
	}
}

func TestClientIP_XForwardedFor_NotFromProxy(t *testing.T) {
	proxies := parseCIDRs([]string{"172.16.0.0/12"})

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "8.8.8.8:12345"
	r.Header.Set("X-Forwarded-For", "192.168.1.5")

	ip := clientIP(r, proxies)
	if ip.String() != "8.8.8.8" {
		t.Errorf("got %v, want 8.8.8.8 (XFF ignored, direct not a proxy)", ip)
	}
}

func TestTrustedNetworkMiddleware_Trusted(t *testing.T) {
	nets := parseCIDRs([]string{"127.0.0.0/8"})
	principal := auth.Principal{Name: "operator", Email: "op@test.com", Scope: "*"}

	var gotPrincipal auth.Principal
	var hasPrincipal bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrincipal, hasPrincipal = auth.PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := TrustedNetworkMiddleware(nets, nil, principal)(inner)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !hasPrincipal {
		t.Fatal("expected principal to be set for trusted request")
	}
	if gotPrincipal.Scope != "*" {
		t.Errorf("scope = %q, want *", gotPrincipal.Scope)
	}
	if gotPrincipal.Email != "op@test.com" {
		t.Errorf("email = %q, want op@test.com", gotPrincipal.Email)
	}
}

func TestTrustedNetworkMiddleware_Untrusted(t *testing.T) {
	nets := parseCIDRs([]string{"127.0.0.0/8"})
	principal := auth.Principal{Name: "operator", Scope: "*"}

	var hasPrincipal bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasPrincipal = auth.PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := TrustedNetworkMiddleware(nets, nil, principal)(inner)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "8.8.8.8:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if hasPrincipal {
		t.Fatal("expected NO principal for untrusted request")
	}
}

func TestTrustedNetworkMiddleware_Empty(t *testing.T) {
	principal := auth.Principal{Name: "operator", Scope: "*"}

	var hasPrincipal bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasPrincipal = auth.PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := TrustedNetworkMiddleware(nil, nil, principal)(inner)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if hasPrincipal {
		t.Fatal("expected NO principal when trusted_networks is empty")
	}
}

func TestTrustedNetworkMiddleware_XFF(t *testing.T) {
	nets := parseCIDRs([]string{"192.168.0.0/16"})
	proxies := parseCIDRs([]string{"172.16.0.0/12"})
	principal := auth.Principal{Name: "operator", Scope: "*"}

	var hasPrincipal bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasPrincipal = auth.PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := TrustedNetworkMiddleware(nets, proxies, principal)(inner)

	// Request from proxy with trusted client in XFF → trusted.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.16.0.1:12345"
	r.Header.Set("X-Forwarded-For", "192.168.1.5")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !hasPrincipal {
		t.Fatal("expected principal for trusted XFF client")
	}

	// Request from proxy with external client in XFF → not trusted.
	hasPrincipal = false
	r = httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.16.0.1:12345"
	r.Header.Set("X-Forwarded-For", "8.8.8.8")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if hasPrincipal {
		t.Fatal("expected NO principal for external XFF client")
	}
}

func TestBasicAuthMiddleware_SkipWhenPrincipalExists(t *testing.T) {
	// This tests the git.BasicAuthMiddleware change: it should skip auth
	// when a principal already exists on the context (e.g. from trusted network).
	// We test this indirectly through the trusted network + git handler flow.

	nets := parseCIDRs([]string{"127.0.0.0/8"})
	principal := auth.Principal{Name: "operator", Email: "op@test.com", Scope: "*"}

	validate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}

	var reached bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	// Wrap: trusted → BasicAuth → inner
	// BasicAuth has a validator that rejects everything, but trusted should bypass it.
	handler := TrustedNetworkMiddleware(nets, nil, principal)(
		basicAuthWrap(validate, inner))

	r := httptest.NewRequest("GET", "/info/refs", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !reached {
		t.Fatal("trusted request should bypass BasicAuth and reach inner handler")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	// Same request from untrusted IP without credentials → 401.
	reached = false
	r = httptest.NewRequest("GET", "/info/refs", nil)
	r.RemoteAddr = "8.8.8.8:12345"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if reached {
		t.Fatal("untrusted request without credentials should NOT reach inner handler")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// basicAuthWrap is a test helper that mimics git.BasicAuthMiddleware's behavior
// (checking for existing principal before requiring Basic Auth).
func basicAuthWrap(validate func(string) (auth.Principal, auth.RejectReason, bool), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.PrincipalFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		_, password, ok := r.BasicAuth()
		if !ok || password == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitYard"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		p, _, valid := validate(password)
		if !valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		ctx := auth.WithPrincipal(r.Context(), p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
