package reload

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/balancer"
	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/health"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l4"
	"github.com/Hoot-Code/Hoot-Lb/internal/proxy/l7"
	lbruntime "github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

func TestConcurrentReloadBurst(t *testing.T) {
	httpAddrA, stopHA := httpIdentBackend(t)
	defer stopHA()
	httpAddrB, stopHB := httpIdentBackend(t)
	defer stopHB()
	tcpAddrA, stopTA := tcpIdentBackend(t)
	defer stopTA()
	tcpAddrB, stopTB := tcpIdentBackend(t)
	defer stopTB()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, fmt.Sprintf(reloadCfgTemplate,
		"tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", tcpAddrA, tcpAddrB))

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	snap, err := lbruntime.BuildSnapshot(cfg, testLogger())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	snapPtr := lbruntime.NewAtomicSnapshot(snap)

	// Start L4 TCP proxy.
	tcpListenerCfg := cfg.Listeners[0]
	tcpGetter := l4.PoolStateGetter(func() (balancer.LoadBalancer, health.FailureReporter, lbruntime.Outcome) {
		ps := snapPtr.Load().PoolStates[tcpListenerCfg.Pool]
		return ps.LB, ps.FR, nil
	})
	tcpSrv, err := l4.NewTCPServer(tcpListenerCfg, tcpGetter, testLogger(), nil, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("NewTCPServer: %v", err)
	}
	go tcpSrv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tcpSrv.Close(ctx)
	}()

	// Start L7 HTTP proxy.
	httpCfgPath := filepath.Join(dir, "http_config.yaml")
	writeConfigContent(t, httpCfgPath, fmt.Sprintf(httpReloadCfgTemplate, httpAddrA, httpAddrB))
	httpCfgParsed, err := config.Load(httpCfgPath)
	if err != nil {
		t.Fatalf("load http cfg: %v", err)
	}
	httpSnap, err := lbruntime.BuildSnapshot(httpCfgParsed, testLogger())
	if err != nil {
		t.Fatalf("build http snapshot: %v", err)
	}
	httpSnapPtr := lbruntime.NewAtomicSnapshot(httpSnap)

	httpListenerCfg := httpCfgParsed.Listeners[0]
	httpGetter := l7.RouteTableGetter(func() *l7.RouteTable {
		return buildTestRouteTable(httpListenerCfg, httpSnapPtr.Load())
	})
	httpSrv, err := l7.NewL7ServerFromGetter(httpListenerCfg, httpGetter, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewL7Server: %v", err)
	}
	go httpSrv.Serve(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Close(ctx)
	}()

	reloadCfgs := [2]string{
		fmt.Sprintf(reloadCfgTemplate, "tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", tcpAddrB, tcpAddrA),
		fmt.Sprintf(reloadCfgTemplate, "tcp_proxy", "127.0.0.1:0", "tcp", "pool_a", tcpAddrA, tcpAddrB),
	}
	httpReloadCfgs := [2]string{
		fmt.Sprintf(httpReloadCfgTemplate, httpAddrB, httpAddrA),
		fmt.Sprintf(httpReloadCfgTemplate, httpAddrA, httpAddrB),
	}

	httpWatcher := NewWatcher(httpCfgPath, 0, httpSnapPtr, httpSnap.CertStores, testLogger(), nil)
	tcpWatcher := NewWatcher(cfgPath, 0, snapPtr, snap.CertStores, testLogger(), nil)

	var reloadCount atomic.Int64
	stopReloads := make(chan struct{})

	go func() {
		i := 0
		for {
			select {
			case <-stopReloads:
				return
			default:
			}
			writeConfig(t, dir, reloadCfgs[i%2])
			tcpWatcher.TriggerReload()
			writeConfigContent(t, httpCfgPath, httpReloadCfgs[i%2])
			httpWatcher.TriggerReload()
			reloadCount.Add(1)
			i++
			time.Sleep(time.Millisecond)
		}
	}()

	var reqWg sync.WaitGroup
	var failCount atomic.Int64
	const numRequests = 100

	for i := 0; i < numRequests; i++ {
		reqWg.Add(1)
		go func(n int) {
			defer reqWg.Done()
			conn, err := net.DialTimeout("tcp", tcpSrv.Addr().String(), 2*time.Second)
			if err != nil {
				failCount.Add(1)
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprintf(conn, "msg-%d\n", n)
			sc := bufio.NewScanner(conn)
			if !sc.Scan() {
				failCount.Add(1)
				return
			}
			got := strings.TrimSpace(sc.Text())
			if got != tcpAddrA && got != tcpAddrB {
				failCount.Add(1)
			}
		}(i)
	}

	httpClient := &http.Client{Timeout: 3 * time.Second}
	for i := 0; i < numRequests; i++ {
		reqWg.Add(1)
		go func(n int) {
			defer reqWg.Done()
			resp, err := httpClient.Get(fmt.Sprintf("http://%s/%d", httpSrv.Addr().String(), n))
			if err != nil {
				failCount.Add(1)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			got := string(body)
			if got != httpAddrA && got != httpAddrB {
				failCount.Add(1)
			}
		}(i)
	}

	reqWg.Wait()
	close(stopReloads)
	time.Sleep(100 * time.Millisecond)

	t.Logf("completed %d reloads with %d TCP + %d HTTP requests, %d failures",
		reloadCount.Load(), numRequests, numRequests, failCount.Load())

	if failCount.Load() > 0 {
		t.Errorf("%d requests failed during concurrent reload burst", failCount.Load())
	}
}
