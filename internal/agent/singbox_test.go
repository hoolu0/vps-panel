package agent

import (
	"errors"
	"strings"
	"testing"
)

var errExec = errors.New("fork/exec /usr/local/bin/sing-box: no such file or directory")

func TestEnsureRunnableCheckOK(t *testing.T) {
	installs := 0
	err := ensureRunnableWith(&Runner{},
		func() (string, error) { return "sing-box version 1.14.4", nil },
		false,
		func() error { installs++; return nil })
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if installs != 0 {
		t.Fatalf("install called %d times, want 0", installs)
	}
}

func TestEnsureRunnableAlpineFixedByGcompat(t *testing.T) {
	calls, installs := 0, 0
	check := func() (string, error) {
		calls++
		if calls == 1 {
			return "", errExec
		}
		return "sing-box version 1.14.4", nil
	}
	err := ensureRunnableWith(&Runner{}, check, true,
		func() error { installs++; return nil })
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if installs != 1 {
		t.Fatalf("install called %d times, want 1", installs)
	}
	if calls != 2 {
		t.Fatalf("check called %d times, want 2", calls)
	}
}

func TestEnsureRunnableAlpineStillBroken(t *testing.T) {
	check := func() (string, error) { return "", errExec }
	err := ensureRunnableWith(&Runner{}, check, true, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "仍无法运行") {
		t.Fatalf("expected 仍无法运行 error, got %v", err)
	}
}

func TestEnsureRunnableNonAlpine(t *testing.T) {
	installs := 0
	check := func() (string, error) { return "", errExec }
	err := ensureRunnableWith(&Runner{}, check, false,
		func() error { installs++; return nil })
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if installs != 0 {
		t.Fatalf("install called %d times on non-Alpine, want 0", installs)
	}
}
