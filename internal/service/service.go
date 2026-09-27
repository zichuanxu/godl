// Package service composes the store, queue manager, and loopback API.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/zichuanxu/nimget/internal/api"
	"github.com/zichuanxu/nimget/internal/download"
	"github.com/zichuanxu/nimget/internal/engine"
	"github.com/zichuanxu/nimget/internal/manager"
	"github.com/zichuanxu/nimget/internal/netproxy"
	"github.com/zichuanxu/nimget/internal/secrets"
	"github.com/zichuanxu/nimget/internal/store"
)

type Config struct {
	Address      string
	DatabasePath string
	// TokenPath holds the API token; it defaults to DefaultTokenPath.
	TokenPath string
	// DownloadRoots confine destinations; they default to DefaultDownloadRoot.
	DownloadRoots []string
	// Runner defaults to the engine, using the proxy from the settings.
	Runner download.Runner
	Logger *slog.Logger
	// Publish also receives every manager event; it must not block.
	Publish func(download.Event)
	// Sealer encrypts stored secrets; it defaults to a key in the OS keyring.
	Sealer *secrets.Sealer
}

type Service struct {
	cfg Config
	log *slog.Logger

	store    *store.SQLite
	manager  *manager.Manager
	server   *http.Server
	listener net.Listener

	cancelRun context.CancelFunc
	runDone   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error

	serveMu  sync.Mutex
	serveErr error
}

func New(cfg Config) (*Service, error) {
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:51000"
	}
	if cfg.DatabasePath == "" {
		path, err := DefaultDatabasePath()
		if err != nil {
			return nil, err
		}
		cfg.DatabasePath = path
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.TokenPath == "" {
		path, err := DefaultTokenPath()
		if err != nil {
			return nil, err
		}
		cfg.TokenPath = path
	}
	if len(cfg.DownloadRoots) == 0 {
		root, err := DefaultDownloadRoot()
		if err != nil {
			return nil, err
		}
		cfg.DownloadRoots = []string{root}
	}
	token, err := LoadOrCreateToken(cfg.TokenPath)
	if err != nil {
		return nil, err
	}
	if cfg.Sealer == nil {
		sealer, err := secrets.Open()
		if err != nil {
			return nil, err
		}
		if sealer.KeyringErr != nil {
			cfg.Logger.Warn("secrets kept in memory only", "err", sealer.KeyringErr)
		}
		cfg.Sealer = sealer
	}
	db, err := store.Open(cfg.DatabasePath, cfg.Sealer)
	if err != nil {
		return nil, err
	}
	// The proxy reads the manager's settings on every connection; no request
	// is made before New returns.
	var mgr *manager.Manager
	if cfg.Runner == nil {
		runner, err := engine.NewRunner(engine.Config{
			Proxy: netproxy.Func(func() netproxy.Config { return mgr.Settings().Proxy }),
		})
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		cfg.Runner = runner
	}
	broker := api.NewBroker(256)
	publish := broker.Publish
	if extra := cfg.Publish; extra != nil {
		publish = func(e download.Event) {
			broker.Publish(e)
			extra(e)
		}
	}
	mgr, err = manager.New(db, cfg.Runner, manager.Options{
		Publish:       publish,
		Logger:        cfg.Logger,
		DownloadRoots: cfg.DownloadRoots,
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Service{
		cfg: cfg, log: cfg.Logger, store: db, manager: mgr,
		server: &http.Server{
			Handler:           api.NewHandler(mgr, broker, api.Config{Token: token, Logger: cfg.Logger}),
			ReadHeaderTimeout: 10 * time.Second,
		},
		closed: make(chan struct{}),
	}, nil
}

// Start listens on the loopback address and runs the queue. The service stops
// when ctx is canceled, Close is called, or the listener fails.
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

	// The manager gets its own context so Close controls the shutdown order:
	// stop serving, let runners checkpoint, then close the store.
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancelRun = cancel
	s.runDone = make(chan struct{})
	go func() {
		defer close(s.runDone)
		s.manager.Run(runCtx)
	}()
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("serve loopback API", "err", err)
			s.serveMu.Lock()
			s.serveErr = err
			s.serveMu.Unlock()
		}
		_ = s.Close()
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closed:
		}
	}()
	s.log.Info("service listening", "address", listener.Addr().String())
	return nil
}

// Manager is the queue, for hosts that call it in process.
func (s *Service) Manager() *manager.Manager {
	return s.manager
}

func (s *Service) Address() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Wait blocks until the service has fully stopped and returns the listener
// error, if that is what stopped it.
func (s *Service) Wait() error {
	<-s.closed
	s.serveMu.Lock()
	defer s.serveMu.Unlock()
	return s.serveErr
}

// Close stops the API, waits for every runner to record its final state, and
// then closes the store.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.server.Close()
		if s.cancelRun != nil {
			s.cancelRun()
			<-s.runDone
		}
		if err := s.store.Close(); s.closeErr == nil {
			s.closeErr = err
		}
		close(s.closed)
		s.log.Info("service stopped")
	})
	return s.closeErr
}
