//go:build k8s

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

// K8s implements the Discovery interface for Kubernetes Endpoints/EndpointSlice
// resolution via the REST API. It uses raw HTTP calls only — no client SDK.
type K8s struct {
	name      string
	namespace string
	service   string
	logger    *slog.Logger
	client    *http.Client
}

// NewK8s creates a Kubernetes discovery adapter. kubeconfigPath is
// unused when running in-cluster (token from service account).
func NewK8s(name, namespace, service, kubeconfigPath string, logger *slog.Logger) *K8s {
	token := readServiceAccountToken()

	return &K8s{
		name:      name,
		namespace: namespace,
		service:   service,
		logger:    logger,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &k8sTransport{
				token: token,
			},
		},
	}
}

type k8sTransport struct {
	token string
}

func (t *k8sTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	req.URL.Scheme = "https"
	req.URL.Host = "kubernetes.default.svc"
	return http.DefaultTransport.RoundTrip(req)
}

func readServiceAccountToken() string {
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return ""
	}
	return string(data)
}

type endpointSliceResponse struct {
	Endpoints []struct {
		Addresses  []string `json:"addresses"`
		Conditions struct {
			Ready bool `json:"ready"`
		} `json:"conditions"`
	} `json:"endpoints"`
}

type endpointsResponse struct {
	Subsets []struct {
		Addresses []struct {
			IP string `json:"ip"`
		} `json:"addresses"`
	} `json:"subsets"`
}

func (k *K8s) Resolve(ctx context.Context) ([]Backend, error) {
	backends, err := k.resolveEndpointSlice(ctx)
	if err == nil && len(backends) > 0 {
		return backends, nil
	}
	return k.resolveEndpoints(ctx)
}

func (k *K8s) resolveEndpointSlice(ctx context.Context) ([]Backend, error) {
	url := fmt.Sprintf("/apis/discovery.k8s.io/v1/namespaces/%s/endpointslices?labelSelector=kubernetes.io/service-name=%s",
		k.namespace, k.service)

	resp, err := k.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("k8s endpointslice: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("k8s endpointslice %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Items []endpointSliceResponse `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("k8s endpointslice decode: %w", err)
	}

	var backends []Backend
	for _, item := range result.Items {
		for _, ep := range item.Endpoints {
			if !ep.Conditions.Ready {
				continue
			}
			for _, addr := range ep.Addresses {
				if net.ParseIP(addr) != nil {
					backends = append(backends, Backend{Address: net.JoinHostPort(addr, "80"), Weight: 1})
				}
			}
		}
	}

	k.logger.Debug("k8s endpointslice resolution",
		slog.String(logging.ComponentKey, "discovery"),
		slog.String("service", k.service),
		slog.Int("backends", len(backends)))
	return backends, nil
}

func (k *K8s) resolveEndpoints(ctx context.Context) ([]Backend, error) {
	url := fmt.Sprintf("/api/v1/namespaces/%s/endpoints/%s", k.namespace, k.service)

	resp, err := k.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("k8s endpoints: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("k8s endpoints %d: %s", resp.StatusCode, body)
	}

	var result endpointsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("k8s endpoints decode: %w", err)
	}

	var backends []Backend
	for _, subset := range result.Subsets {
		for _, addr := range subset.Addresses {
			backends = append(backends, Backend{Address: net.JoinHostPort(addr.IP, "80"), Weight: 1})
		}
	}

	k.logger.Debug("k8s endpoints resolution",
		slog.String(logging.ComponentKey, "discovery"),
		slog.String("service", k.service),
		slog.Int("backends", len(backends)))
	return backends, nil
}

func (k *K8s) Name() string { return k.name }
