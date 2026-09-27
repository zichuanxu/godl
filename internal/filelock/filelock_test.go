package filelock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const helperEnv = "GODL_FILELOCK_HELPER"

// TestMain lets the test binary act as a lock-holding child process.
func TestMain(m *testing.M) {
	if path := os.Getenv(helperEnv); path != "" {
		if _, err := Acquire(path); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Stdout.WriteString("locked\n")
		select {} // hold the lock until killed
	}
	os.Exit(m.Run())
}

func TestAcquireTwiceInProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.lock")
	release, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire: got %v, want ErrLocked", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file not removed: %v", err)
	}
	release, err = Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	_ = release()
}

func TestStaleLockFileDoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.lock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire with stale lock file: %v", err)
	}
	_ = release()
}

func TestLockReleasedWhenHolderKilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"="+path)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("child did not report lock: %q, %v", line, err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("Acquire while child holds lock: got %v, want ErrLocked", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// Windows releases a dead process's locks asynchronously, so allow a moment.
	deadline := time.Now().Add(2 * time.Second)
	for {
		release, err := Acquire(path)
		if err == nil {
			_ = release()
			return
		}
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			t.Fatalf("Acquire after child was killed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
