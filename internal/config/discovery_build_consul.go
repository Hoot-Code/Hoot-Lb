//go:build consul

package config

// consulAvailable indicates whether the consul discovery type is
// compiled into the binary. When built with -tags consul, the consul
// adapter is available.
var consulAvailable = true
