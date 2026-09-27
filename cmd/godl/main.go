package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zichuanxu/godl/downloader"
	"github.com/zichuanxu/godl/internal/client"
	"github.com/zichuanxu/godl/internal/engine"
	"github.com/zichuanxu/godl/internal/service"
	"github.com/spf13/cobra"
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

type cliConfig struct {
	address string
}

func main() {
	log.SetFlags(0)
	cfg := &cliConfig{address: "http://127.0.0.1:51000"}
	root := &cobra.Command{Use: "godl", Short: "A local download manager"}
	root.AddCommand(newServiceCommand(), newAddCommand(cfg), newListCommand(cfg), newPauseCommand(cfg), newDownloadCommand())
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

func newServiceCommand() *cobra.Command {
	var address, database string
	var maxConcurrent int
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Run the background download service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if database == "" {
				var err error
				database, err = service.DefaultDatabasePath()
				if err != nil {
					return err
				}
			}
			runner, err := engine.NewRunner(downloader.Config{})
			if err != nil {
				return err
			}
			svc, err := service.New(service.Config{Address: address, DatabasePath: database, Runner: runner, MaxConcurrent: maxConcurrent})
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := svc.Start(ctx); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "godl service listening on %s\n", svc.Address())
			<-ctx.Done()
			return svc.Close()
		},
	}
	cmd.Flags().StringVar(&address, "address", "127.0.0.1:51000", "loopback address for the service")
	cmd.Flags().StringVar(&database, "database", "", "SQLite database path")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 3, "maximum active downloads")
	return cmd
}

func newAddCommand(cfg *cliConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add URL OUTPUT",
		Short: "Queue a download in the service",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			item, err := client.New(cfg.address, nil).Add(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return writeJSON(cmd, item)
		},
	}
	cmd.Flags().StringVar(&cfg.address, "address", cfg.address, "service base URL")
	return cmd
}

func newListCommand(cfg *cliConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List service downloads",
		RunE: func(cmd *cobra.Command, _ []string) error {
			items, err := client.New(cfg.address, nil).List(cmd.Context())
			if err != nil {
				return err
			}
			return writeJSON(cmd, items)
		},
	}
	cmd.Flags().StringVar(&cfg.address, "address", cfg.address, "service base URL")
	return cmd
}

func newPauseCommand(cfg *cliConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pause ID",
		Short: "Pause a service download",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return client.New(cfg.address, nil).Pause(cmd.Context(), args[0])
		},
	}
	cmd.Flags().StringVar(&cfg.address, "address", cfg.address, "service base URL")
	return cmd
}

func newDownloadCommand() *cobra.Command {
	var workers, attempts int
	var chunkMiB, parallelMiB int64
	var partTimeout time.Duration
	var sha256sum, resumeKey string
	var overwrite bool
	var headers headerFlags
	cmd := &cobra.Command{
		Use:   "download URL OUTPUT",
		Short: "Download a file directly without the service",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if chunkMiB <= 0 || parallelMiB < 0 {
				return errors.New("chunk-mib must be positive and parallel-min-mib cannot be negative")
			}
			h := make(http.Header)
			for _, raw := range headers {
				name, value, _ := strings.Cut(raw, ":")
				name, value = strings.TrimSpace(name), strings.TrimSpace(value)
				if name == "" {
					return fmt.Errorf("invalid empty header name in %q", raw)
				}
				h.Add(name, value)
			}
			dl, err := downloader.New(downloader.Config{Workers: workers, ChunkSize: chunkMiB << 20, MinParallelSize: parallelMiB << 20, MaxAttempts: attempts, PartTimeout: partTimeout, Headers: h, ExpectedSHA256: sha256sum, ResumeKey: resumeKey, Overwrite: overwrite})
			if err != nil {
				return err
			}
			var lastLineLen int
			err = dl.Download(cmd.Context(), args[0], args[1], func(p downloader.Progress) {
				line := fmt.Sprintf("%6.2f%%  %s / %s", p.Percent, formatBytes(p.Completed), formatBytes(p.Total))
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
	cmd.Flags().IntVar(&workers, "workers", 8, "maximum concurrent range workers")
	cmd.Flags().Int64Var(&chunkMiB, "chunk-mib", 8, "range chunk size in MiB")
	cmd.Flags().Int64Var(&parallelMiB, "parallel-min-mib", 32, "minimum file size for parallel mode")
	cmd.Flags().IntVar(&attempts, "attempts", 5, "maximum attempts per request")
	cmd.Flags().DurationVar(&partTimeout, "part-timeout", 2*time.Minute, "maximum duration of one range request")
	cmd.Flags().StringVar(&sha256sum, "sha256", "", "optional expected SHA-256 hex digest")
	cmd.Flags().StringVar(&resumeKey, "resume-key", "", "stable resume identity for expiring signed URLs")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace an existing destination")
	cmd.Flags().Var(&headers, "header", "request header; repeatable, for example -header 'Authorization: Bearer ...'")
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
