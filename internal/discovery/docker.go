//go:build docker

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
)

const dockerSocket = "/var/run/docker.sock"

// Docker implements the Discovery interface for Docker container
// resolution via the Docker daemon's REST API over Unix socket.
type Docker struct {
	name          string
	network       string
	labelSelector string
	logger        *slog.Logger
	client        *http.Client
}

// NewDocker creates a Docker discovery adapter.
func NewDocker(name, network, labelSelector string, logger *slog.Logger) *Docker {
	return &Docker{
		name:          name,
		network:       network,
		labelSelector: labelSelector,
		logger:        logger,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", dockerSocket)
				},
			},
		},
	}
}

type dockerContainerResponse struct {
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (d *Docker) Resolve(ctx context.Context) ([]Backend, error) {
	containers, err := d.listContainers(ctx)
	if err != nil {
		return nil, err
	}

	var backends []Backend
	for _, id := range containers {
		ip, err := d.getContainerIP(ctx, id)
		if err != nil {
			d.logger.Warn("failed to get container IP",
				slog.String(logging.ComponentKey, "discovery"),
				slog.String("container", id[:12]),
				slog.String("error", err.Error()))
			continue
		}
		if ip != "" {
			backends = append(backends, Backend{Address: net.JoinHostPort(ip, "80"), Weight: 1})
		}
	}

	d.logger.Debug("docker resolution",
		slog.String(logging.ComponentKey, "discovery"),
		slog.String("network", d.network),
		slog.Int("backends", len(backends)))
	return backends, nil
}

func (d *Docker) listContainers(ctx context.Context) ([]string, error) {
	filters := url.QueryEscape(fmt.Sprintf(`{"label":["%s"]}`, d.labelSelector))
	apiURL := fmt.Sprintf("http://localhost/containers/json?filters=%s", filters)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("docker list request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker list %d: %s", resp.StatusCode, body)
	}

	var containers []struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, fmt.Errorf("docker list decode: %w", err)
	}

	ids := make([]string, len(containers))
	for i, c := range containers {
		ids[i] = c.ID
	}
	return ids, nil
}

func (d *Docker) getContainerIP(ctx context.Context, containerID string) (string, error) {
	apiURL := fmt.Sprintf("http://localhost/containers/%s/json", containerID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("docker inspect request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("docker inspect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("docker inspect %d: %s", resp.StatusCode, body)
	}

	var container dockerContainerResponse
	if err := json.NewDecoder(resp.Body).Decode(&container); err != nil {
		return "", fmt.Errorf("docker inspect decode: %w", err)
	}

	if network, ok := container.NetworkSettings.Networks[d.network]; ok {
		return network.IPAddress, nil
	}

	return "", nil
}

func (d *Docker) Name() string { return d.name }

// parseLabelSelector parses a simple "key=value,key2=value2" selector.
func parseLabelSelector(selector string) map[string]string {
	labels := make(map[string]string)
	for _, part := range strings.Split(selector, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			labels[kv[0]] = kv[1]
		}
	}
	return labels
}
