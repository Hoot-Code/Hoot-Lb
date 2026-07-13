//go:build !docker

package config

// dockerAvailable indicates whether the docker discovery type is
// compiled into the binary. In the default build (no docker tag),
// docker is not available.
var dockerAvailable = false
