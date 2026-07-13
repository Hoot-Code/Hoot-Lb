package l7

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

func echoIdentBackend(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ident backend listen: %v", err)
	}
	addr := ln.Addr().String()
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "%s", addr)
		}),
	}
	go srv.Serve(ln)
	return addr, func() { srv.Close() }
}

func TestStickyHappyPath(t *testing.T) {
	addrA, stopA := echoIdentBackend(t)
	defer stopA()
	addrB, stopB := echoIdentBackend(t)
	defer stopB()

	serverA := balancer.NewServer(addrA, 1)
	serverB := balancer.NewServer(addrB, 1)
	lb := balancer.NewRoundRobin([]balancer.Backend{serverA, serverB})

	members := map[string]bool{addrA: true, addrB: true}
	sticky := &config.StickyConfig{
		CookieName: "test_sticky",
		TTL:        1 * time.Hour,
	}

	routes := []Route{
		{
			PathPrefix:  "/sticky",
			LB:          lb,
			Sticky:      sticky,
			PoolMembers: func(addr string) bool { return members[addr] },
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lb)
	defer stopProxy()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}

	resp1, err := client.Get(fmt.Sprintf("http://%s/sticky/first", proxyAddr))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	backend1 := string(body1)

	if backend1 != addrA && backend1 != addrB {
		t.Fatalf("first request hit unexpected backend: %s", backend1)
	}

	cookies1 := resp1.Cookies()
	var stickyCookie *http.Cookie
	for _, c := range cookies1 {
		if c.Name == "test_sticky" {
			stickyCookie = c
			break
		}
	}
	if stickyCookie == nil {
		t.Fatal("first response missing Set-Cookie header")
	}

	resp2, err := client.Get(fmt.Sprintf("http://%s/sticky/second", proxyAddr))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	backend2 := string(body2)

	if backend2 != backend1 {
		t.Errorf("sticky broken: first=%s, second=%s", backend1, backend2)
	}
}

func TestStickyGracefulDegradationRemovedBackend(t *testing.T) {
	addrA, stopA := echoIdentBackend(t)
	defer stopA()
	addrB, stopB := echoIdentBackend(t)
	defer stopB()

	serverA := balancer.NewServer(addrA, 1)
	serverB := balancer.NewServer(addrB, 1)
	lb := balancer.NewRoundRobin([]balancer.Backend{serverA, serverB})

	members := map[string]bool{addrA: true, addrB: true}
	sticky := &config.StickyConfig{
		CookieName: "test_sticky",
		TTL:        1 * time.Hour,
	}

	routes := []Route{
		{
			PathPrefix:  "/sticky",
			LB:          lb,
			Sticky:      sticky,
			PoolMembers: func(addr string) bool { return members[addr] },
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lb)
	defer stopProxy()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}

	resp1, err := client.Get(fmt.Sprintf("http://%s/sticky/first", proxyAddr))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	backend1 := string(body1)

	delete(members, backend1)

	resp2, err := client.Get(fmt.Sprintf("http://%s/sticky/second", proxyAddr))
	if err != nil {
		t.Fatalf("second request after removal: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	backend2 := string(body2)

	if backend2 == backend1 {
		t.Errorf("request should have degraded to a different backend, still got %s", backend2)
	}

	cookies2 := resp2.Cookies()
	var freshCookie *http.Cookie
	for _, c := range cookies2 {
		if c.Name == "test_sticky" {
			freshCookie = c
			break
		}
	}
	if freshCookie == nil {
		t.Fatal("degraded response missing fresh Set-Cookie header")
	}
	decoded, _ := url.QueryUnescape(freshCookie.Value)
	if decoded != backend2 {
		t.Errorf("fresh cookie value=%s, want=%s", decoded, backend2)
	}
}

func TestStickyUnhealthyBackend(t *testing.T) {
	addrA, stopA := echoIdentBackend(t)
	defer stopA()
	addrB, stopB := echoIdentBackend(t)
	defer stopB()

	serverA := balancer.NewServer(addrA, 1)
	serverB := balancer.NewServer(addrB, 1)
	lb := balancer.NewRoundRobin([]balancer.Backend{serverA, serverB})

	members := map[string]bool{addrA: true, addrB: true}
	sticky := &config.StickyConfig{
		CookieName: "test_sticky",
		TTL:        1 * time.Hour,
	}

	routes := []Route{
		{
			PathPrefix: "/sticky",
			LB:         lb,
			Sticky:     sticky,
			PoolMembers: func(addr string) bool {
				if !members[addr] {
					return false
				}
				if addr == addrA && !serverA.IsHealthy() {
					return false
				}
				return true
			},
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lb)
	defer stopProxy()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}

	resp1, err := client.Get(fmt.Sprintf("http://%s/sticky/first", proxyAddr))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	backend1 := string(body1)

	serverA.SetHealthy(false)

	resp2, err := client.Get(fmt.Sprintf("http://%s/sticky/second", proxyAddr))
	if err != nil {
		t.Fatalf("second request after unhealthy: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	backend2 := string(body2)

	if backend2 == backend1 {
		t.Errorf("request should have degraded from unhealthy backend, still got %s", backend2)
	}
}

func TestStickyTamperedCookie(t *testing.T) {
	addrA, stopA := echoIdentBackend(t)
	defer stopA()

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(addrA, 1)})

	routes := []Route{
		{
			PathPrefix:  "/sticky",
			LB:          lb,
			Sticky:      &config.StickyConfig{CookieName: "test_sticky", TTL: 1 * time.Hour},
			PoolMembers: func(addr string) bool { return addr == addrA },
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lb)
	defer stopProxy()

	req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/sticky/tampered", proxyAddr), nil)
	req.AddCookie(&http.Cookie{Name: "test_sticky", Value: "garbage_value_not_an_address"})

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request with tampered cookie: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != addrA {
		t.Errorf("expected backend %s, got %s", addrA, string(body))
	}
}
