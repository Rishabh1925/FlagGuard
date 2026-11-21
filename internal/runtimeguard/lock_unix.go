//go:build linux || darwin

package runtimeguard

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock excludes a second local process using the same state path. Keep the lock
// inode in place: unlinking it would allow another process to lock a new inode.
// This is a local-filesystem guarantee, not distributed leader election.
func Lock(state string) (func(), error) {
	if state == "" {
		return nil, fmt.Errorf("state_file is required for a running controller")
	}
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(state+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state is already in use or cannot be locked: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
