//go:build !consul && k8s && !docker

package runtime

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
)

func newDiscoveryAdapter(p config.PoolConfig, logger *slog.Logger) (discovery.Discovery, error) {
	if p.Discovery == nil {
		return nil, fmt.Errorf("no discovery config")
	}
	switch p.Discovery.Type {
	case "dns":
		if p.Discovery.DNS == nil {
			return nil, fmt.Errorf("dns config missing")
		}
		return discovery.NewDNS(p.Name, net.DefaultResolver, p.Discovery.DNS.Host, p.Discovery.DNS.Port, logger), nil
	case "consul":
		return nil, fmt.Errorf("consul discovery requires building with -tags consul")
	case "k8s":
		if p.Discovery.K8s == nil {
			return nil, fmt.Errorf("k8s config missing")
		}
		return newK8sAdapter(p.Name, p.Discovery.K8s, logger), nil
	case "docker":
		return nil, fmt.Errorf("docker discovery requires building with -tags docker")
	default:
		return nil, fmt.Errorf("unknown discovery type %q", p.Discovery.Type)
	}
}
