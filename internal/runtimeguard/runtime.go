// Package runtimeguard validates startup settings and loads file-backed secrets.
package runtimeguard

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// LoadSecrets supports Docker/Kubernetes mounted secrets without printing values.
func LoadSecrets(names ...string) error {
	for _, name := range names {
		file := os.Getenv(name + "_FILE")
		if file == "" {
			continue
		}
		if os.Getenv(name) != "" {
			return fmt.Errorf("set only one of %s and %s_FILE", name, name)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("cannot read %s_FILE", name)
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return fmt.Errorf("%s_FILE is empty", name)
		}
		if err = os.Setenv(name, value); err != nil {
			return err
		}
	}
	return nil
}

func ValidateListener(addr, token string, allowInsecure bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address must contain a host and port")
	}
	if token != "" && len(token) < 32 {
		return fmt.Errorf("FLAGGUARD_AUTH_TOKEN must have at least 32 characters")
	}
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if !loopback && token == "" && !allowInsecure {
		return fmt.Errorf("non-loopback binding requires FLAGGUARD_AUTH_TOKEN; use explicit FLAGGUARD_LOCAL_DEMO=true only with loopback-published demo ports")
	}
	return nil
}
