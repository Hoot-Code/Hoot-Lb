//go:build docker

package config

// dockerAvailable indicates whether the docker discovery type is
// compiled into the binary. When built with -tags docker, the docker
// adapter is available.
var dockerAvailable = true
