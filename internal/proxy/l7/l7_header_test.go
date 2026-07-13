package l7

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
)

func TestL7HeaderRouting(t *testing.T) {
	backendA, stopA := echoIdentBackend(t)
	defer stopA()
	backendB, stopB := echoIdentBackend(t)
	defer stopB()

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})

	routes := []Route{
		{
			PathPrefix:  "/beta",
			HeaderName:  "X-Beta-User",
			HeaderValue: "true",
			LB:          lbB,
		},
		{
			PathPrefix: "/beta",
			LB:         lbA,
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lbA)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	req1, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/beta/test", proxyAddr), nil)
	req1.Header.Set("X-Beta-User", "true")
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("request with header: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if string(body1) != backendB {
		t.Errorf("header match: got backend %s, want %s", string(body1), backendB)
	}

	resp2, err := client.Get(fmt.Sprintf("http://%s/beta/test", proxyAddr))
	if err != nil {
		t.Fatalf("request without header: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != backendA {
		t.Errorf("no header match: got backend %s, want %s", string(body2), backendA)
	}

	req3, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/beta/test", proxyAddr), nil)
	req3.Header.Set("X-Beta-User", "false")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("request with wrong header value: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if string(body3) != backendA {
		t.Errorf("wrong header value: got backend %s, want %s", string(body3), backendA)
	}
}

func TestL7HeaderRoutingCombinedConditions(t *testing.T) {
	backendA, stopA := echoIdentBackend(t)
	defer stopA()
	backendB, stopB := echoIdentBackend(t)
	defer stopB()

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})

	routes := []Route{
		{
			Host:        "api.example.com",
			PathPrefix:  "/v2",
			HeaderName:  "X-Debug",
			HeaderValue: "1",
			LB:          lbB,
		},
		{
			Host:       "api.example.com",
			PathPrefix: "/v2",
			LB:         lbA,
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lbA)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	req1, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/v2/test", proxyAddr), nil)
	req1.Host = "api.example.com"
	req1.Header.Set("X-Debug", "1")
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("all conditions: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if string(body1) != backendB {
		t.Errorf("all conditions: got %s, want %s", string(body1), backendB)
	}

	req2, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/v2/test", proxyAddr), nil)
	req2.Host = "api.example.com"
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("missing header: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != backendA {
		t.Errorf("missing header: got %s, want %s", string(body2), backendA)
	}

	req3, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/v2/test", proxyAddr), nil)
	req3.Host = "other.example.com"
	req3.Header.Set("X-Debug", "1")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("wrong host: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if string(body3) != backendA {
		t.Errorf("wrong host: got %s, want default %s", string(body3), backendA)
	}
}

func TestL7SplitDistribution(t *testing.T) {
	backendA, stopA := echoIdentBackend(t)
	defer stopA()
	backendB, stopB := echoIdentBackend(t)
	defer stopB()

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})

	routes := []Route{
		{
			PathPrefix: "/canary",
			Split: []SplitEntry{
				{LB: lbA, FR: nil, Weight: 90},
				{LB: lbB, FR: nil, Weight: 10},
			},
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lbA)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	const numRequests = 10000
	counts := map[string]int{backendA: 0, backendB: 0}

	for i := 0; i < numRequests; i++ {
		resp, err := client.Get(fmt.Sprintf("http://%s/canary/%d", proxyAddr, i))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		counts[string(body)]++
	}

	ratioA := float64(counts[backendA]) / float64(numRequests)
	ratioB := float64(counts[backendB]) / float64(numRequests)

	if ratioA < 0.85 || ratioA > 0.95 {
		t.Errorf("backend A ratio %.2f, want ~0.90 (counts: %v)", ratioA, counts)
	}
	if ratioB < 0.05 || ratioB > 0.15 {
		t.Errorf("backend B ratio %.2f, want ~0.10 (counts: %v)", ratioB, counts)
	}
}

func TestL7SplitThreeWay(t *testing.T) {
	backendA, stopA := echoIdentBackend(t)
	defer stopA()
	backendB, stopB := echoIdentBackend(t)
	defer stopB()
	backendC, stopC := echoIdentBackend(t)
	defer stopC()

	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendA, 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendB, 1)})
	lbC := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer(backendC, 1)})

	routes := []Route{
		{
			PathPrefix: "/split3",
			Split: []SplitEntry{
				{LB: lbA, Weight: 50},
				{LB: lbB, Weight: 30},
				{LB: lbC, Weight: 20},
			},
		},
	}

	proxyAddr, stopProxy := startL7Proxy(t, routes, lbA)
	defer stopProxy()

	client := &http.Client{Timeout: 5 * time.Second}

	const numRequests = 15000
	counts := map[string]int{backendA: 0, backendB: 0, backendC: 0}

	for i := 0; i < numRequests; i++ {
		resp, err := client.Get(fmt.Sprintf("http://%s/split3/%d", proxyAddr, i))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		counts[string(body)]++
	}

	ratioA := float64(counts[backendA]) / float64(numRequests)
	ratioB := float64(counts[backendB]) / float64(numRequests)
	ratioC := float64(counts[backendC]) / float64(numRequests)

	if ratioA < 0.45 || ratioA > 0.55 {
		t.Errorf("backend A ratio %.2f, want ~0.50 (counts: %v)", ratioA, counts)
	}
	if ratioB < 0.25 || ratioB > 0.35 {
		t.Errorf("backend B ratio %.2f, want ~0.30 (counts: %v)", ratioB, counts)
	}
	if ratioC < 0.15 || ratioC > 0.25 {
		t.Errorf("backend C ratio %.2f, want ~0.20 (counts: %v)", ratioC, counts)
	}
}

func TestRouteTableHeaderMatch(t *testing.T) {
	lbA := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:1", 1)})
	lbB := balancer.NewRoundRobin([]balancer.Backend{balancer.NewServer("127.0.0.1:2", 1)})

	routes := []Route{
		{
			PathPrefix:  "/api",
			HeaderName:  "X-Version",
			HeaderValue: "v2",
			LB:          lbB,
		},
		{
			PathPrefix: "/api",
			LB:         lbA,
		},
	}

	table := NewRouteTable(routes, lbA, nil)

	h := http.Header{}
	h.Set("X-Version", "v2")
	route := table.Match("example.com", "/api/test", h)
	if route.LB != lbB {
		t.Error("expected route 0 for matching header")
	}

	h2 := http.Header{}
	h2.Set("X-Version", "v1")
	route2 := table.Match("example.com", "/api/test", h2)
	if route2.LB != lbA {
		t.Error("expected route 1 for non-matching header")
	}

	route3 := table.Match("example.com", "/api/test", http.Header{})
	if route3.LB != lbA {
		t.Error("expected route 1 when header is absent")
	}
}

func TestPickSplitEntryWeights(t *testing.T) {
	entries := []SplitEntry{
		{Weight: 70},
		{Weight: 20},
		{Weight: 10},
	}

	counts := make(map[int]int)
	const iterations = 10000
	for i := 0; i < iterations; i++ {
		e := pickSplitEntry(entries)
		counts[e.Weight]++
	}

	ratio70 := float64(counts[70]) / float64(iterations)
	if ratio70 < 0.65 || ratio70 > 0.75 {
		t.Errorf("weight-70 ratio %.2f, want ~0.70", ratio70)
	}

	if counts[10] == 0 || counts[20] == 0 || counts[70] == 0 {
		t.Errorf("not all entries selected: %v", counts)
	}
}
