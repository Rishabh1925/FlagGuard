//go:build !linux && !darwin

package runtimeguard

import "fmt"

func Lock(string) (func(), error) {
	return nil, fmt.Errorf("run the controller in Linux Docker; native state locking is supported on Linux and macOS")
}
