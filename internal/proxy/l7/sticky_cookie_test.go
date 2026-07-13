package l7

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
)

func TestStickyWithIPHash(t *testing.T) {
	addrA, stopA := echoIdentBackend(t)
	defer stopA()
	addrB, stopB := echoIdentBackend(t)
	defer stopB()

	serverA := balancer.NewServer(addrA, 1)
	serverB := balancer.NewServer(addrB, 1)
	lb := balancer.NewIPHash([]balancer.Backend{serverA, serverB})

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

	cookies1 := resp1.Cookies()
	var found bool
	for _, c := range cookies1 {
		if c.Name == "test_sticky" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing Set-Cookie in first response")
	}

	resp2, err := client.Get(fmt.Sprintf("http://%s/sticky/second", proxyAddr))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	backend2 := string(body2)

	if backend2 != backend1 {
		t.Errorf("sticky + ip_hash broken: first=%s, second=%s", backend1, backend2)
	}
}

func TestStickyCookieSecureForTLS(t *testing.T) {
	addrA, stopA := echoIdentBackend(t)
	defer stopA()

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(addrA, 1)})

	cert := generateCert(t, []string{""})

	routes := []Route{
		{
			PathPrefix:  "/sticky",
			LB:          lb,
			Sticky:      &config.StickyConfig{CookieName: "my_cookie", TTL: 2 * time.Hour},
			PoolMembers: func(addr string) bool { return addr == addrA },
		},
	}

	proxyAddr, stopProxy := startTLSTerminationProxy(t, []config.TLSCertConfig{cert}, routes, lb)
	defer stopProxy()

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Get(fmt.Sprintf("https://%s/sticky/attrs", proxyAddr))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	var cookieHeader string
	for _, h := range resp.Header["Set-Cookie"] {
		if strings.Contains(h, "my_cookie=") {
			cookieHeader = h
			break
		}
	}
	if cookieHeader == "" {
		t.Fatal("missing Set-Cookie header for my_cookie")
	}

	lower := strings.ToLower(cookieHeader)

	if !strings.Contains(lower, "secure") {
		t.Error("TLS cookie should have Secure flag")
	}
	if !strings.Contains(lower, "httponly") {
		t.Error("cookie missing HttpOnly flag")
	}
	if !strings.Contains(lower, "samesite=lax") {
		t.Error("cookie missing SameSite=Lax")
	}
}

func TestStickyCookieAttributes(t *testing.T) {
	addrA, stopA := echoIdentBackend(t)
	defer stopA()

	lb := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(addrA, 1)})

	routes := []Route{
		{
			PathPrefix:  "/sticky",
			LB:          lb,
			Sticky:      &config.StickyConfig{CookieName: "my_cookie", TTL: 2 * time.Hour},
			PoolMembers: func(addr string) bool { return addr == addrA },
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lb)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/sticky/attrs", proxyAddr))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	var cookieHeader string
	for _, h := range resp.Header["Set-Cookie"] {
		if strings.Contains(h, "my_cookie=") {
			cookieHeader = h
			break
		}
	}
	if cookieHeader == "" {
		t.Fatal("missing Set-Cookie header for my_cookie")
	}

	lower := strings.ToLower(cookieHeader)

	if strings.Contains(lower, "secure") {
		t.Error("plain HTTP cookie should not have Secure flag")
	}
	if !strings.Contains(lower, "httponly") {
		t.Error("cookie missing HttpOnly flag")
	}
	if !strings.Contains(lower, "samesite=lax") {
		t.Error("cookie missing SameSite=Lax")
	}
	if !strings.Contains(lower, "path=/") {
		t.Error("cookie missing Path=/")
	}
	if !strings.Contains(lower, "max-age=7200") {
		t.Errorf("cookie Max-Age should be 7200 (2h), got: %s", cookieHeader)
	}
}
