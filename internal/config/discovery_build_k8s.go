//go:build k8s

package config

// k8sAvailable indicates whether the k8s discovery type is
// compiled into the binary. When built with -tags k8s, the k8s
// adapter is available.
var k8sAvailable = true
