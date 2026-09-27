// Package service composes the store, queue manager, and loopback API.
package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/zichuanxu/godl/internal/api"
	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/manager"
	"github.com/zichuanxu/godl/internal/store"
)

type Config struct {
	Address       string
	DatabasePath  string
	Runner        download.Runner
	MaxConcurrent int
}

type Service struct {
	cfg Config

	store    *store.SQLite
	manager  *manager.Manager
	server   *http.Server
	listener net.Listener

	closeOnce sync.Once
	closeErr  error
}

func New(cfg Config) (*Service, error) {
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:51000"
	}
	if cfg.DatabasePath == "" {
		return nil, errors.New("database path is required")
	}
	if cfg.Runner == nil {
		return nil, errors.New("download runner is required")
	}
	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	broker := api.NewBroker(256)
	mgr, err := manager.New(db, cfg.Runner, manager.Options{
		MaxConcurrent: cfg.MaxConcurrent,
		Publish:       broker.Publish,
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Service{
		cfg: cfg, store: db, manager: mgr,
		server: &http.Server{Handler: api.NewHandler(mgr, broker)},
	}, nil
}

func (s *Service) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	listener, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Address, err)
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddress.IP.IsLoopback() {
		_ = listener.Close()
		return errors.New("service must listen on a loopback address")
	}
	s.listener = listener
	go s.manager.Run(ctx)
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = s.Close()
		}
	}()
	return nil
}

func (s *Service) Address() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Service) Wait() error {
	if s.listener == nil {
		return errors.New("service is not started")
	}
	return errors.New("service wait is managed by Start context")
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		if s.server != nil {
			s.closeErr = s.server.Close()
		}
		if err := s.store.Close(); s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}
