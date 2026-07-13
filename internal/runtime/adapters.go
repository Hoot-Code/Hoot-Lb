package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// consulAdapter resolves backends via Consul's health API.
type consulAdapter struct {
	name, address, service string
	logger                 *slog.Logger
	client                 *http.Client
}

func newConsulAdapter(name string, cfg *config.ConsulDiscoveryConfig, logger *slog.Logger) *consulAdapter {
	return &consulAdapter{name: name, address: cfg.Address, service: cfg.Service, logger: logger, client: &http.Client{Timeout: 10 * time.Second}}
}

type consulServiceEntry struct {
	Service struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Service"`
}

type consulHealthResponse []consulServiceEntry

func (c *consulAdapter) Resolve(ctx context.Context) ([]discovery.Backend, error) {
	url := fmt.Sprintf("%s/v1/health/service/%s?passing=true", c.address, c.service)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("consul request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("consul %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("consul %d: %s", resp.StatusCode, body)
	}
	var entries consulHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("consul decode: %w", err)
	}
	backends := make([]discovery.Backend, 0, len(entries))
	for _, e := range entries {
		addr := e.Service.Address
		if addr == "" {
			addr = "127.0.0.1"
		}
		if e.Service.Port > 0 {
			addr = net.JoinHostPort(addr, strconv.Itoa(e.Service.Port))
		}
		backends = append(backends, discovery.Backend{Address: addr, Weight: 1})
	}
	c.logger.Debug("consul resolution", slog.String(logging.ComponentKey, "discovery"), slog.String("service", c.service), slog.Int("backends", len(backends)))
	return backends, nil
}

func (c *consulAdapter) Name() string { return c.name }

// k8sAdapter resolves backends via Kubernetes Endpoints/EndpointSlice API.
type k8sAdapter struct {
	name, namespace, service string
	logger                   *slog.Logger
	client                   *http.Client
}

func newK8sAdapter(name string, cfg *config.K8sDiscoveryConfig, logger *slog.Logger) *k8sAdapter {
	token, _ := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return &k8sAdapter{name: name, namespace: cfg.Namespace, service: cfg.Service, logger: logger,
		client: &http.Client{Timeout: 10 * time.Second, Transport: &k8sTransport{token: string(token)}}}
}

type k8sTransport struct{ token string }

func (t *k8sTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	req.URL.Scheme, req.URL.Host = "https", "kubernetes.default.svc"
	return http.DefaultTransport.RoundTrip(req)
}

func (k *k8sAdapter) Resolve(ctx context.Context) ([]discovery.Backend, error) {
	// Try EndpointSlice first.
	url := fmt.Sprintf("/apis/discovery.k8s.io/v1/namespaces/%s/endpointslices?labelSelector=kubernetes.io/service-name=%s", k.namespace, k.service)
	resp, err := k.client.Get(url)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var result struct {
				Items []struct {
					Endpoints []struct {
						Addresses  []string
						Conditions struct{ Ready bool }
					}
				}
			}
			if json.NewDecoder(resp.Body).Decode(&result) == nil {
				var backends []discovery.Backend
				for _, item := range result.Items {
					for _, ep := range item.Endpoints {
						if !ep.Conditions.Ready {
							continue
						}
						for _, addr := range ep.Addresses {
							if net.ParseIP(addr) != nil {
								backends = append(backends, discovery.Backend{Address: net.JoinHostPort(addr, "80"), Weight: 1})
							}
						}
					}
				}
				if len(backends) > 0 {
					k.logger.Debug("k8s resolution", slog.String(logging.ComponentKey, "discovery"), slog.String("service", k.service), slog.Int("backends", len(backends)))
					return backends, nil
				}
			}
		}
	}
	// Fall back to legacy Endpoints.
	url = fmt.Sprintf("/api/v1/namespaces/%s/endpoints/%s", k.namespace, k.service)
	resp, err = k.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("k8s endpoints: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("k8s %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Subsets []struct{ Addresses []struct{ IP string } }
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("k8s decode: %w", err)
	}
	var backends []discovery.Backend
	for _, s := range result.Subsets {
		for _, a := range s.Addresses {
			backends = append(backends, discovery.Backend{Address: net.JoinHostPort(a.IP, "80"), Weight: 1})
		}
	}
	k.logger.Debug("k8s resolution", slog.String(logging.ComponentKey, "discovery"), slog.String("service", k.service), slog.Int("backends", len(backends)))
	return backends, nil
}

func (k *k8sAdapter) Name() string { return k.name }

// dockerAdapter resolves backends via Docker daemon REST API over Unix socket.
type dockerAdapter struct {
	name, network, labelSelector string
	logger                       *slog.Logger
	client                       *http.Client
}

func newDockerAdapter(name string, cfg *config.DockerDiscoveryConfig, logger *slog.Logger) *dockerAdapter {
	return &dockerAdapter{name: name, network: cfg.Network, labelSelector: cfg.LabelSelector, logger: logger,
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", "/var/run/docker.sock")
			},
		}}}
}

func (d *dockerAdapter) Resolve(ctx context.Context) ([]discovery.Backend, error) {
	filters := fmt.Sprintf(`{"label":["%s"]}`, d.labelSelector)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost/containers/json?filters="+url.QueryEscape(filters), nil)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker %d: %s", resp.StatusCode, body)
	}
	var containers []struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&containers)

	var backends []discovery.Backend
	for _, c := range containers {
		req2, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost/containers/"+c.ID+"/json", nil)
		resp2, err := d.client.Do(req2)
		if err != nil {
			continue
		}
		var info struct {
			NetworkSettings struct {
				Networks map[string]struct{ IPAddress string }
			}
		}
		json.NewDecoder(resp2.Body).Decode(&info)
		resp2.Body.Close()
		if n, ok := info.NetworkSettings.Networks[d.network]; ok && n.IPAddress != "" {
			backends = append(backends, discovery.Backend{Address: net.JoinHostPort(n.IPAddress, "80"), Weight: 1})
		}
	}
	d.logger.Debug("docker resolution", slog.String(logging.ComponentKey, "discovery"), slog.String("network", d.network), slog.Int("backends", len(backends)))
	return backends, nil
}

func (d *dockerAdapter) Name() string { return d.name }
