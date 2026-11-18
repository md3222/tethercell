
//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Stub host: "<stub> run <mode>" exits 3/4/2 or sleeps until SIGTERM.
func stub(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "host")
	err := os.WriteFile(p, []byte(`#!/bin/sh
case "$2" in
e2) exit 2;; e3) echo dying; exit 3;; e4) exit 4;;
sleep) trap 'echo got-term; exit 0' TERM; sleep 30 & wait; exit 9;;
esac
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunHostExitCodes(t *testing.T) {
	exe := stub(t)
	for mode, want := range map[string]int{"e2": 2, "e3": 3, "e4": 4} {
		rc, stopped := runHost(exe, []string{mode}, make(chan os.Signal, 1))
		if rc != want || stopped {
			t.Errorf("%s: rc=%d stopped=%v, want %d false", mode, rc, stopped, want)
		}
	}
}

func TestRunHostForwardsStop(t *testing.T) {
	exe := stub(t)
	sig := make(chan os.Signal, 1)
	go func() { time.Sleep(300 * time.Millisecond); sig <- syscall.SIGINT }()
	t0 := time.Now()
	rc, stopped := runHost(exe, []string{"sleep"}, sig)
	if !stopped || rc != 0 {
		t.Fatalf("rc=%d stopped=%v, want 0 true", rc, stopped)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("child took %s to stop (SIGTERM not forwarded?)", d)
	}
}
