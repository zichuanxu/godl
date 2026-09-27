package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zichuanxu/godl/internal/client"
	"github.com/zichuanxu/godl/internal/download"
	"github.com/zichuanxu/godl/internal/engine"
	"github.com/zichuanxu/godl/internal/logging"
	"github.com/zichuanxu/godl/internal/netproxy"
	"github.com/zichuanxu/godl/internal/service"
	"github.com/zichuanxu/godl/internal/settings"
	"golang.org/x/time/rate"
)

type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ", ") }
func (h *headerFlags) Type() string   { return "header" }

func (h *headerFlags) Set(value string) error {
	if !strings.Contains(value, ":") {
		return fmt.Errorf("header must use 'Name: value' syntax")
	}
	*h = append(*h, value)
	return nil
}

// values returns the headers by canonical name.
func (h headerFlags) values() (map[string]string, error) {
	out := make(map[string]string, len(h))
	for _, raw := range h {
		name, value, _ := strings.Cut(raw, ":")
		name, value = http.CanonicalHeaderKey(strings.TrimSpace(name)), strings.TrimSpace(value)
		if err := settings.ValidateHeader(name, value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, nil
}

// Set at link time by goreleaser: -X main.version=... -X main.commit=... -X main.date=...
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type cliConfig struct {
	address   string
	tokenFile string
}

func main() {
	log.SetFlags(0)
	cfg := &cliConfig{address: "http://127.0.0.1:51000"}
	root := &cobra.Command{Use: "godl", Short: "A local download manager", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&cfg.address, "address", cfg.address, "service base URL")
	root.PersistentFlags().StringVar(&cfg.tokenFile, "token-file", "", "service token file (default: in the godl data directory)")
	root.AddCommand(
		newServiceCommand(cfg),
		newAddCommand(cfg),
		newListCommand(cfg),
		newIDCommand(cfg, "pause", "Pause a service download", (*client.Client).Pause),
		newIDCommand(cfg, "resume", "Resume a paused download", (*client.Client).Resume),
		newIDCommand(cfg, "retry", "Retry a failed download", (*client.Client).Retry),
		newSetCommand(cfg),
		newDeleteCommand(cfg),
		newSettingsCommand(cfg),
		newDownloadCommand(),
		newVersionCommand(),
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "godl %s (commit %s, built %s)\n", version, commit, date)
		},
	}
}

func newServiceCommand(cfg *cliConfig) *cobra.Command {
	var address, database, logLevel string
	var roots []string
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run the background download service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var level slog.Level
			if err := level.UnmarshalText([]byte(logLevel)); err != nil {
				return fmt.Errorf("invalid --log-level: %w", err)
			}
			if database == "" {
				var err error
				if database, err = service.DefaultDatabasePath(); err != nil {
					return err
				}
			}
			svc, err := service.New(service.Config{
				Address: address, DatabasePath: database, TokenPath: cfg.tokenFile,
				DownloadRoots: roots, Logger: logging.New(cmd.ErrOrStderr(), level),
			})
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := svc.Start(ctx); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "godl service listening on %s\n", svc.Address())
			return svc.Wait()
		},
	}
	cmd.Flags().StringVar(&address, "listen", "127.0.0.1:51000", "loopback address for the service")
	cmd.Flags().StringVar(&database, "database", "", "SQLite database path")
	cmd.Flags().StringArrayVar(&roots, "download-root", nil, "directory downloads may be written under; repeatable (default: ~/Downloads)")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, or error")
	return cmd
}

func (cfg *cliConfig) client() (*client.Client, error) {
	path := cfg.tokenFile
	if path == "" {
		var err error
		if path, err = service.DefaultTokenPath(); err != nil {
			return nil, err
		}
	}
	token, err := service.ReadToken(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no service token at %s; start the service with 'godl service' first", path)
	}
	if err != nil {
		return nil, err
	}
	return client.New(cfg.address, token, nil), nil
}

func newAddCommand(cfg *cliConfig) *cobra.Command {
	var dir, priority, speed, checksum string
	var connections int
	var headers headerFlags
	cmd := &cobra.Command{
		Use:   "add URL [OUTPUT]",
		Short: "Queue a download in the service",
		Long: "Queue a download in the service. Without OUTPUT the file name comes from the server\n" +
			"and the file goes to --dir, or to a category folder under the download root.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cfg.client()
			if err != nil {
				return err
			}
			req := download.Request{URL: args[0], Connections: connections, Checksum: checksum}
			if req.Priority, err = parsePriority(priority); err != nil {
				return err
			}
			if req.SpeedLimit, err = parseSpeed(speed); err != nil {
				return err
			}
			if req.Headers, err = headers.values(); err != nil {
				return err
			}
			// The service runs in another working directory.
			if len(args) == 2 {
				if req.Destination, err = filepath.Abs(args[1]); err != nil {
					return err
				}
			}
			if dir != "" {
				if req.Directory, err = filepath.Abs(dir); err != nil {
					return err
				}
			}
			item, err := c.Add(cmd.Context(), req)
			if err != nil {
				return err
			}
			return writeJSON(cmd, item)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory for a server-named file")
	cmd.Flags().StringVar(&priority, "priority", "normal", "low, normal, or high")
	cmd.Flags().IntVar(&connections, "connections", 0, "connections, 1-32 (default: site or global setting)")
	cmd.Flags().StringVar(&speed, "speed-limit", "0", "speed limit in bytes per second, with K, M, or G suffix; 0 for none")
	cmd.Flags().StringVar(&checksum, "checksum", "", "expected digest as algo:hex (sha256, sha512, sha1, md5)")
	cmd.Flags().Var(&headers, "header", "request header, such as a cookie; repeatable")
	return cmd
}

func newSetCommand(cfg *cliConfig) *cobra.Command {
	var priority, speed string
	var connections int
	cmd := &cobra.Command{
		Use:   "set ID",
		Short: "Change a download's priority, connections, or speed limit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cfg.client()
			if err != nil {
				return err
			}
			var patch download.Patch
			if cmd.Flags().Changed("priority") {
				p, err := parsePriority(priority)
				if err != nil {
					return err
				}
				patch.Priority = &p
			}
			if cmd.Flags().Changed("speed-limit") {
				limit, err := parseSpeed(speed)
				if err != nil {
					return err
				}
				patch.SpeedLimit = &limit
			}
			if cmd.Flags().Changed("connections") {
				patch.Connections = &connections
			}
			return c.Update(cmd.Context(), args[0], patch)
		},
	}
	cmd.Flags().StringVar(&priority, "priority", "", "low, normal, or high")
	cmd.Flags().IntVar(&connections, "connections", 0, "connections, 1-32; 0 for the default")
	cmd.Flags().StringVar(&speed, "speed-limit", "", "speed limit in bytes per second, with K, M, or G suffix; 0 for none")
	return cmd
}

func newSettingsCommand(cfg *cliConfig) *cobra.Command {
	cmd := &cobra.Command{Use: "settings", Short: "Show or replace the service settings"}
	cmd.AddCommand(&cobra.Command{
		Use:   "get",
		Short: "Print the settings as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := cfg.client()
			if err != nil {
				return err
			}
			s, err := c.Settings(cmd.Context())
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(s)
		},
	}, &cobra.Command{
		Use:   "set FILE",
		Short: "Replace the settings with a JSON file ('-' for standard input)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cfg.client()
			if err != nil {
				return err
			}
			var in io.Reader = cmd.InOrStdin()
			if args[0] != "-" {
				f, err := os.Open(args[0])
				if err != nil {
					return err
				}
				defer f.Close()
				in = f
			}
			var s settings.Settings
			dec := json.NewDecoder(in)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&s); err != nil {
				return fmt.Errorf("parse settings: %w", err)
			}
			saved, err := c.PutSettings(cmd.Context(), s)
			if err != nil {
				return err
			}
			return writeJSON(cmd, saved)
		},
	})
	return cmd
}

func parsePriority(value string) (int, error) {
	switch strings.ToLower(value) {
	case "low":
		return download.PriorityLow, nil
	case "", "normal":
		return download.PriorityNormal, nil
	case "high":
		return download.PriorityHigh, nil
	}
	return 0, fmt.Errorf("priority must be low, normal, or high, got %q", value)
}

// parseSpeed reads bytes per second with an optional binary K, M, or G suffix.
func parseSpeed(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	value = strings.TrimSuffix(strings.TrimSuffix(value, "/S"), "B")
	shift := 0
	if n := len(value); n > 0 {
		if i := strings.IndexByte("KMG", value[n-1]); i >= 0 {
			shift, value = 10*(i+1), value[:n-1]
		}
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid speed %q; use for example 500K or 2M", value)
	}
	return int64(n * float64(int64(1)<<shift)), nil
}

func newListCommand(cfg *cliConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List service downloads",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := cfg.client()
			if err != nil {
				return err
			}
			items, err := c.List(cmd.Context())
			if err != nil {
				return err
			}
			return writeJSON(cmd, items)
		},
	}
}

func newIDCommand(cfg *cliConfig, name, short string, run func(*client.Client, context.Context, string) error) *cobra.Command {
	return &cobra.Command{
		Use:   name + " ID",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cfg.client()
			if err != nil {
				return err
			}
			return run(c, cmd.Context(), args[0])
		},
	}
}

func newDeleteCommand(cfg *cliConfig) *cobra.Command {
	var removeFiles bool
	cmd := &cobra.Command{
		Use:   "delete ID",
		Short: "Delete a stopped download from the queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cfg.client()
			if err != nil {
				return err
			}
			return c.Delete(cmd.Context(), args[0], removeFiles)
		},
	}
	cmd.Flags().BoolVar(&removeFiles, "files", false, "also remove partial data and, for completed downloads, the file")
	return cmd
}

func newDownloadCommand() *cobra.Command {
	var connections, attempts int
	var minSplitMiB int64
	var stallTimeout time.Duration
	var checksum, resumeKey, speed, proxy string
	var overwrite bool
	var headers headerFlags
	cmd := &cobra.Command{
		Use:   "download URL OUTPUT",
		Short: "Download a file directly without the service",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			values, err := headers.values()
			if err != nil {
				return err
			}
			h := make(http.Header)
			for name, value := range values {
				h.Set(name, value)
			}
			limit, err := parseSpeed(speed)
			if err != nil {
				return err
			}
			var limiters []*rate.Limiter
			if limit > 0 {
				limiters = append(limiters, rate.NewLimiter(rate.Limit(limit), 256<<10))
			}
			proxyCfg := netproxy.Config{Mode: netproxy.Mode(proxy)}
			if strings.Contains(proxy, "://") {
				proxyCfg = netproxy.Config{Mode: netproxy.ModeManual, URL: proxy}
			}
			if err := proxyCfg.Validate(); err != nil {
				return err
			}
			e, err := engine.New(engine.Config{
				Connections: connections, MinSplitSize: minSplitMiB << 20, MaxAttempts: attempts,
				StallTimeout: stallTimeout, Headers: h, Checksum: checksum, ResumeKey: resumeKey, Overwrite: overwrite,
				Limiters: limiters, Proxy: netproxy.Func(func() netproxy.Config { return proxyCfg }),
			})
			if err != nil {
				return err
			}
			var lastLineLen int
			err = e.Download(cmd.Context(), args[0], args[1], func(p engine.Progress) {
				line := formatBytes(p.Completed) + " / " + formatBytes(p.Total)
				if p.Total > 0 {
					line = fmt.Sprintf("%6.2f%%  %s", float64(p.Completed)*100/float64(p.Total), line)
				}
				padding := ""
				if lastLineLen > len(line) {
					padding = strings.Repeat(" ", lastLineLen-len(line))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "\r%s%s", line, padding)
				lastLineLen = len(line)
			})
			if lastLineLen > 0 {
				fmt.Fprintln(cmd.ErrOrStderr())
			}
			return err
		},
	}
	cmd.Flags().IntVar(&connections, "connections", 8, "parallel connections, at most 32")
	cmd.Flags().Int64Var(&minSplitMiB, "min-split-mib", 1, "smallest range in MiB handed to one connection")
	cmd.Flags().IntVar(&attempts, "attempts", 5, "consecutive failed requests without progress before giving up")
	cmd.Flags().DurationVar(&stallTimeout, "stall-timeout", 30*time.Second, "fail a connection that receives no bytes for this long")
	cmd.Flags().StringVar(&checksum, "checksum", "", "expected digest as algo:hex (sha256, sha512, sha1, md5)")
	cmd.Flags().StringVar(&resumeKey, "resume-key", "", "stable resume identity for expiring signed URLs")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace an existing destination")
	cmd.Flags().Var(&headers, "header", "request header; repeatable, for example --header 'Authorization: Bearer ...'")
	cmd.Flags().StringVar(&speed, "speed-limit", "0", "speed limit in bytes per second, with K, M, or G suffix; 0 for none")
	cmd.Flags().StringVar(&proxy, "proxy", "system", "system, none, or a proxy URL (http://, https://, socks5://)")
	return cmd
}

func writeJSON(cmd *cobra.Command, value any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
}

func formatBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit && exp < 5; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
