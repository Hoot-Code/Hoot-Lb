//go:build !k8s

package config

// k8sAvailable indicates whether the k8s discovery type is
// compiled into the binary. In the default build (no k8s tag),
// k8s is not available.
var k8sAvailable = false
