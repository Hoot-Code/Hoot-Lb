package config

// validDiscoveryTypes enumerates the discovery types accepted in
// pools[].discovery.type.
var validDiscoveryTypes = map[string]bool{
	"dns":    true,
	"consul": true,
	"k8s":    true,
	"docker": true,
}

// validateDiscovery checks a discovery configuration for semantic
// correctness: valid type, required sub-blocks, and build-tag gating.
func validateDiscovery(dc *DiscoveryConfig, prefix string) []error {
	var errs []error

	if !validDiscoveryTypes[dc.Type] {
		errs = append(errs, newFieldError(prefix+".type", "must be one of: dns, consul, k8s, docker (got %q)", dc.Type))
	}

	switch dc.Type {
	case "dns":
		if dc.DNS == nil {
			errs = append(errs, newFieldError(prefix+".dns", "is required when type is dns"))
		} else {
			errs = append(errs, validateDNSDiscovery(dc.DNS, prefix+".dns")...)
		}
		if dc.Consul != nil {
			errs = append(errs, newFieldError(prefix+".consul", "consul config is not valid when type is dns"))
		}
		if dc.K8s != nil {
			errs = append(errs, newFieldError(prefix+".k8s", "k8s config is not valid when type is dns"))
		}
		if dc.Docker != nil {
			errs = append(errs, newFieldError(prefix+".docker", "docker config is not valid when type is dns"))
		}

	case "consul":
		if !consulAvailable {
			errs = append(errs, newFieldError(prefix+".type", "consul discovery requires building with -tags consul"))
			return errs
		}
		if dc.Consul == nil {
			errs = append(errs, newFieldError(prefix+".consul", "is required when type is consul"))
		} else {
			errs = append(errs, validateConsulDiscovery(dc.Consul, prefix+".consul")...)
		}
		if dc.DNS != nil {
			errs = append(errs, newFieldError(prefix+".dns", "dns config is not valid when type is consul"))
		}
		if dc.K8s != nil {
			errs = append(errs, newFieldError(prefix+".k8s", "k8s config is not valid when type is consul"))
		}
		if dc.Docker != nil {
			errs = append(errs, newFieldError(prefix+".docker", "docker config is not valid when type is consul"))
		}

	case "k8s":
		if !k8sAvailable {
			errs = append(errs, newFieldError(prefix+".type", "k8s discovery requires building with -tags k8s"))
			return errs
		}
		if dc.K8s == nil {
			errs = append(errs, newFieldError(prefix+".k8s", "is required when type is k8s"))
		} else {
			errs = append(errs, validateK8sDiscovery(dc.K8s, prefix+".k8s")...)
		}
		if dc.DNS != nil {
			errs = append(errs, newFieldError(prefix+".dns", "dns config is not valid when type is k8s"))
		}
		if dc.Consul != nil {
			errs = append(errs, newFieldError(prefix+".consul", "consul config is not valid when type is k8s"))
		}
		if dc.Docker != nil {
			errs = append(errs, newFieldError(prefix+".docker", "docker config is not valid when type is k8s"))
		}

	case "docker":
		if !dockerAvailable {
			errs = append(errs, newFieldError(prefix+".type", "docker discovery requires building with -tags docker"))
			return errs
		}
		if dc.Docker == nil {
			errs = append(errs, newFieldError(prefix+".docker", "is required when type is docker"))
		} else {
			errs = append(errs, validateDockerDiscovery(dc.Docker, prefix+".docker")...)
		}
		if dc.DNS != nil {
			errs = append(errs, newFieldError(prefix+".dns", "dns config is not valid when type is docker"))
		}
		if dc.Consul != nil {
			errs = append(errs, newFieldError(prefix+".consul", "consul config is not valid when type is docker"))
		}
		if dc.K8s != nil {
			errs = append(errs, newFieldError(prefix+".k8s", "k8s config is not valid when type is docker"))
		}
	}

	return errs
}

func validateDNSDiscovery(dns *DNSDiscoveryConfig, prefix string) []error {
	var errs []error

	if dns.Host == "" {
		errs = append(errs, newFieldError(prefix+".host", "is required"))
	}
	if dns.Port < 0 || dns.Port > 65535 {
		errs = append(errs, newFieldError(prefix+".port", "must be between 0 and 65535 (got %d)", dns.Port))
	}
	if dns.RefreshInterval <= 0 {
		errs = append(errs, newFieldError(prefix+".refresh_interval", "must be a positive duration"))
	}

	return errs
}

func validateConsulDiscovery(c *ConsulDiscoveryConfig, prefix string) []error {
	var errs []error

	if c.Address == "" {
		errs = append(errs, newFieldError(prefix+".address", "is required"))
	}
	if c.Service == "" {
		errs = append(errs, newFieldError(prefix+".service", "is required"))
	}
	if c.RefreshInterval <= 0 {
		errs = append(errs, newFieldError(prefix+".refresh_interval", "must be a positive duration"))
	}

	return errs
}

func validateK8sDiscovery(k *K8sDiscoveryConfig, prefix string) []error {
	var errs []error

	if k.Namespace == "" {
		errs = append(errs, newFieldError(prefix+".namespace", "is required"))
	}
	if k.Service == "" {
		errs = append(errs, newFieldError(prefix+".service", "is required"))
	}
	if k.RefreshInterval <= 0 {
		errs = append(errs, newFieldError(prefix+".refresh_interval", "must be a positive duration"))
	}

	return errs
}

func validateDockerDiscovery(d *DockerDiscoveryConfig, prefix string) []error {
	var errs []error

	if d.Network == "" {
		errs = append(errs, newFieldError(prefix+".network", "is required"))
	}
	if d.LabelSelector == "" {
		errs = append(errs, newFieldError(prefix+".label_selector", "is required"))
	}
	if d.RefreshInterval <= 0 {
		errs = append(errs, newFieldError(prefix+".refresh_interval", "must be a positive duration"))
	}

	return errs
}
