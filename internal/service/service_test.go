package service_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/service"
)

type idleRunner struct{}

func (idleRunner) Download(context.Context, string, string, func(download.Progress)) error {
	return nil
}

func TestServiceStartsOnLoopbackAndAnswersPing(t *testing.T) {
	svc, err := service.New(service.Config{
		Address:      "127.0.0.1:0",
		DatabasePath: filepath.Join(t.TempDir(), "godl.db"),
		Runner:       idleRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	resp, err := http.Get("http://" + svc.Address() + "/v1/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ping status = %d", resp.StatusCode)
	}
}
