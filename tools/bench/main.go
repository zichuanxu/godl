//go:build unix

// Command bench measures godl against aria2c on a local server that caps
// each connection's bandwidth, as CDNs do, and checks the M1 performance gate
// in DESIGN.md section 8:
//
//	throughput >= 95% of aria2c, < 1 CPU core at 1 Gbps, RSS < 100 MB
//
// Usage: go build -o godl ./cmd/godl && go run ./tools/bench -godl ./godl -gate
package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type result struct {
	name    string
	wall    time.Duration
	cpu     time.Duration
	maxRSS  int64 // bytes
	bytes   int64
	correct bool
}

func (r result) mbps() float64 { return float64(r.bytes) / r.wall.Seconds() / (1 << 20) }

// coresAt1Gbps normalizes CPU time to a 125 MB/s transfer.
func (r result) coresAt1Gbps() float64 {
	return r.cpu.Seconds() / (float64(r.bytes) / 125e6)
}

func main() {
	godl := flag.String("godl", "./godl", "path to the godl binary")
	aria := flag.String("aria2c", "aria2c", "path to aria2c; skipped when missing")
	sizeMiB := flag.Int("size", 256, "throttled file size in MiB")
	rawMiB := flag.Int("raw-size", 512, "unthrottled file size in MiB")
	rate := flag.Float64("rate", 4, "per-connection cap in MiB/s")
	conns := flag.Int("connections", 16, "connections for both tools")
	gate := flag.Bool("gate", false, "exit non-zero when the gate fails")
	flag.Parse()

	dir, err := os.MkdirTemp("", "godl-bench-")
	check(err)
	defer os.RemoveAll(dir)

	throttled := serve(randomData(*sizeMiB<<20), *rate*(1<<20))
	unthrottled := serve(randomData(*rawMiB<<20), 0)

	// Each figure is the best of three runs, which keeps scheduler noise on
	// shared CI runners out of the 95% comparison.
	var rows []result
	godlThrottled := best(func() result { return runGodl(*godl, throttled, dir, "godl (capped)", *conns) })
	rows = append(rows, godlThrottled)
	var ariaThrottled *result
	if _, err := exec.LookPath(*aria); err == nil {
		r := best(func() result { return runAria(*aria, throttled, dir, "aria2c (capped)", *conns) })
		ariaThrottled = &r
		rows = append(rows, r)
	} else {
		fmt.Printf("aria2c not found (%s); skipping the comparison\n\n", *aria)
	}
	godlRaw := best(func() result { return runGodl(*godl, unthrottled, dir, "godl (uncapped)", *conns) })
	rows = append(rows, godlRaw)
	if ariaThrottled != nil {
		rows = append(rows, best(func() result { return runAria(*aria, unthrottled, dir, "aria2c (uncapped)", *conns) }))
	}

	fmt.Println("| run | MiB/s | wall | CPU | cores at 1 Gbps | max RSS | correct |")
	fmt.Println("| --- | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, r := range rows {
		fmt.Printf("| %s | %.1f | %.2fs | %.2fs | %.2f | %.1f MB | %v |\n",
			r.name, r.mbps(), r.wall.Seconds(), r.cpu.Seconds(), r.coresAt1Gbps(), float64(r.maxRSS)/1e6, r.correct)
	}

	var failures []string
	for _, r := range []result{godlThrottled, godlRaw} {
		if !r.correct {
			failures = append(failures, r.name+": output differs from the source")
		}
		if r.maxRSS >= 100e6 {
			failures = append(failures, fmt.Sprintf("%s: max RSS %.1f MB >= 100 MB", r.name, float64(r.maxRSS)/1e6))
		}
	}
	if c := godlRaw.coresAt1Gbps(); c >= 1 {
		failures = append(failures, fmt.Sprintf("godl uses %.2f cores at 1 Gbps; limit is 1", c))
	}
	if ariaThrottled != nil {
		if ratio := godlThrottled.mbps() / ariaThrottled.mbps(); ratio < 0.95 {
			failures = append(failures, fmt.Sprintf("godl throughput is %.0f%% of aria2c; gate is 95%%", ratio*100))
		} else {
			fmt.Printf("\ngodl throughput is %.0f%% of aria2c\n", ratio*100)
		}
	}
	if len(failures) == 0 {
		fmt.Println("\nperformance gate: PASS")
		return
	}
	fmt.Println("\nperformance gate: FAIL")
	for _, f := range failures {
		fmt.Println("-", f)
	}
	if *gate {
		os.Exit(1)
	}
}

func best(run func() result) result {
	top := run()
	for range 2 {
		if r := run(); r.wall < top.wall {
			top = r
		}
	}
	return top
}

type source struct {
	url  string
	data []byte
	sum  [32]byte
}

func randomData(size int) []byte {
	data := make([]byte, size)
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i+8 <= size; i += 8 {
		v := r.Uint64()
		for b := range 8 {
			data[i+b] = byte(v >> (8 * b))
		}
	}
	return data
}

// serve exposes data with ranges and a strong ETag. A positive rate caps
// every response at that many bytes per second.
func serve(data []byte, rate float64) source {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"bench-v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		start, end := 0, len(data)-1
		status := http.StatusOK
		if spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
			first, last, _ := strings.Cut(spec, "-")
			start, _ = strconv.Atoi(first)
			if last != "" {
				end, _ = strconv.Atoi(last)
			}
			end = min(end, len(data)-1)
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		}
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(status)
		body := data[start : end+1]
		if rate <= 0 {
			_, _ = w.Write(body)
			return
		}
		const piece = 64 << 10
		began := time.Now()
		var sent int
		for sent < len(body) {
			n, err := w.Write(body[sent:min(sent+piece, len(body))])
			sent += n
			if err != nil {
				return
			}
			if ahead := time.Duration(float64(sent)/rate*float64(time.Second)) - time.Since(began); ahead > 0 {
				time.Sleep(ahead)
			}
		}
	})
	go func() { _ = http.Serve(listener, handler) }()
	return source{url: "http://" + listener.Addr().String() + "/file.bin", data: data, sum: sha256.Sum256(data)}
}

func runGodl(bin string, src source, dir, name string, conns int) result {
	out := filepath.Join(dir, strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(name))
	return measure(name, src, out, exec.Command(bin, "download", "--connections", strconv.Itoa(conns), src.url, out))
}

func runAria(bin string, src source, dir, name string, conns int) result {
	out := filepath.Join(dir, strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(name))
	n := strconv.Itoa(conns)
	return measure(name, src, out, exec.Command(bin, "-x"+n, "-s"+n, "-k1M", "--file-allocation=none",
		"--allow-overwrite=true", "--console-log-level=warn", "--summary-interval=0",
		"-d", filepath.Dir(out), "-o", filepath.Base(out), src.url))
}

func measure(name string, src source, out string, cmd *exec.Cmd) result {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	began := time.Now()
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n%s", name, err, stderr.String())
		os.Exit(1)
	}
	r := result{name: name, wall: time.Since(began), bytes: int64(len(src.data))}
	if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		r.cpu = time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
		r.maxRSS = int64(usage.Maxrss)
		if runtime.GOOS == "linux" {
			r.maxRSS *= 1024 // kilobytes on Linux, bytes on macOS
		}
	}
	got, err := os.ReadFile(out)
	r.correct = err == nil && sha256.Sum256(got) == src.sum
	_ = os.Remove(out)
	return r
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
