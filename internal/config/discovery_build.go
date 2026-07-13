//go:build !consul

package config

// consulAvailable indicates whether the consul discovery type is
// compiled into the binary. In the default build (no tags), consul
// is not available.
var consulAvailable = false
