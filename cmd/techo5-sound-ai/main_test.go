package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsASPInvocation(t *testing.T) {
	if !isASPInvocation("/data/techo5-linux/external-asp/bin/external-asp") {
		t.Fatal("expected external-asp basename to be detected as ASP invocation")
	}
	if !isASPInvocation("/proc/self/fd/3") {
		t.Fatal("expected /proc/self/fd/3 to be detected as ASP invocation")
	}
	if isASPInvocation("/data/techo5-sound-ai/techo5-sound-ai") {
		t.Fatal("expected techo5-sound-ai binary not to be detected as ASP invocation")
	}
}

func TestAcquireLockSingleInstanceAndRotateLog(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "sound-ai.lock")

	f1, err := acquireLock(lockPath)
	if err != nil {
		t.Fatalf("first acquireLock failed: %v", err)
	}
	defer f1.Close()

	// Second concurrent acquireLock on the same file must fail with EWOULDBLOCK.
	if f2, err := acquireLock(lockPath); err == nil {
		_ = f2.Close()
		t.Fatal("expected second acquireLock on held lock to fail")
	}

	// Releasing the first lock allows re-acquiring.
	_ = f1.Close()
	f3, err := acquireLock(lockPath)
	if err != nil {
		t.Fatalf("re-acquireLock after close failed: %v", err)
	}
	_ = f3.Close()

	// Log rotation: files <= maxBytes stay in place; files > maxBytes rotate to .1
	logPath := filepath.Join(dir, "sound-ai.log")
	if err := os.WriteFile(logPath, make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}
	rotateLogFile(logPath, 2048)
	if _, err := os.Stat(logPath + ".1"); err == nil {
		t.Fatal("did not expect rotation when size <= maxBytes")
	}
	rotateLogFile(logPath, 512)
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("expected rotated log %s.1 to exist: %v", logPath, err)
	}
}
