package runtimeguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenerBoundary(t *testing.T) {
	for _, tc := range []struct {
		addr, token string
		demo, ok    bool
	}{
		{"127.0.0.1:8085", "", false, true}, {"[::1]:8085", "", false, true},
		{":8085", "", false, false}, {"0.0.0.0:8085", "short", false, false},
		{":8085", strings.Repeat("a", 32), false, true}, {":8085", "", true, true},
	} {
		if (ValidateListener(tc.addr, tc.token, tc.demo) == nil) != tc.ok {
			t.Errorf("incorrect listener decision: %s", tc.addr)
		}
	}
}
func TestFileSecrets(t *testing.T) {
	t.Setenv("TEST_OPERATOR_TOKEN", "")
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("private-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_OPERATOR_TOKEN_FILE", file)
	if err := LoadSecrets("TEST_OPERATOR_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TEST_OPERATOR_TOKEN") != "private-secret" {
		t.Fatal("secret not loaded")
	}
	if LoadSecrets("TEST_OPERATOR_TOKEN") == nil {
		t.Fatal("ambiguous sources accepted")
	}
}
func TestExclusiveStateLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Lock(path); err == nil {
		other()
		t.Fatal("second process lock accepted")
	}
	unlock()
	again, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	again()
}
