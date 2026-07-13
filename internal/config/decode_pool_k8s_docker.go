package config

import (
	"time"
)

func decodeK8sDiscovery(m map[string]interface{}, prefix string) (*K8sDiscoveryConfig, error) {
	k8sPrefix := prefix + ".k8s"

	namespace, ok := stringField(m, "namespace")
	if !ok || namespace == "" {
		return nil, newFieldError(k8sPrefix+".namespace", "is required and must be a non-empty string")
	}

	service, ok := stringField(m, "service")
	if !ok || service == "" {
		return nil, newFieldError(k8sPrefix+".service", "is required and must be a non-empty string")
	}

	cfg := &K8sDiscoveryConfig{
		Namespace: namespace,
		Service:   service,
	}

	if v, ok := m["kubeconfig"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, newFieldError(k8sPrefix+".kubeconfig", "must be a string")
		}
		cfg.Kubeconfig = s
	}

	if raw, ok := m["refresh_interval"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, newFieldError(k8sPrefix+".refresh_interval", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(k8sPrefix+".refresh_interval", "invalid duration %q: %v", s, err)
		}
		cfg.RefreshInterval = d
	}

	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 30 * time.Second
	}

	return cfg, nil
}

func decodeDockerDiscovery(m map[string]interface{}, prefix string) (*DockerDiscoveryConfig, error) {
	dockerPrefix := prefix + ".docker"

	network, ok := stringField(m, "network")
	if !ok || network == "" {
		return nil, newFieldError(dockerPrefix+".network", "is required and must be a non-empty string")
	}

	labelSelector, ok := stringField(m, "label_selector")
	if !ok || labelSelector == "" {
		return nil, newFieldError(dockerPrefix+".label_selector", "is required and must be a non-empty string")
	}

	cfg := &DockerDiscoveryConfig{
		Network:       network,
		LabelSelector: labelSelector,
	}

	if raw, ok := m["refresh_interval"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, newFieldError(dockerPrefix+".refresh_interval", "must be a duration string")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, newFieldError(dockerPrefix+".refresh_interval", "invalid duration %q: %v", s, err)
		}
		cfg.RefreshInterval = d
	}

	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 30 * time.Second
	}

	return cfg, nil
}
