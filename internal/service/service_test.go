package service_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/service"
)

type idleRunner struct{}

func (idleRunner) Download(context.Context, string, string, func(download.Progress)) error {
	return nil
}

func (idleRunner) Discard(string) error { return nil }

func startService(t *testing.T) (*service.Service, string, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	svc, err := service.New(service.Config{
		Address:       "127.0.0.1:0",
		DatabasePath:  filepath.Join(dir, "godl.db"),
		TokenPath:     tokenPath,
		DownloadRoots: []string{filepath.Join(dir, "downloads")},
		Runner:        idleRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = svc.Wait()
	})
	return svc, tokenPath, cancel
}

func get(t *testing.T, url, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestServiceStartsOnLoopbackAndAnswersPing(t *testing.T) {
	svc, _, _ := startService(t)
	if status := get(t, "http://"+svc.Address()+"/v1/ping", ""); status != http.StatusOK {
		t.Fatalf("ping status = %d", status)
	}
}

func TestServiceRequiresItsTokenAndStopsOnCancel(t *testing.T) {
	svc, tokenPath, cancel := startService(t)
	token, err := service.ReadToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(tokenPath); err == nil && info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("token file mode = %v; want owner-only", info.Mode().Perm())
	}
	list := "http://" + svc.Address() + "/v1/downloads"
	if status := get(t, list, ""); status != http.StatusUnauthorized {
		t.Fatalf("list without token = %d; want 401", status)
	}
	if status := get(t, list, token); status != http.StatusOK {
		t.Fatalf("list with token = %d; want 200", status)
	}

	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- svc.Wait() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Wait after cancel = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not stop after its context was canceled")
	}
}

func TestLoadOrCreateTokenIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token")
	first, err := service.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first != second {
		t.Fatalf("tokens = %q, %q; want one stable 64-hex token", first, second)
	}
}
