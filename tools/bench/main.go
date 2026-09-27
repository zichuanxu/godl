//go:build unix

// Command bench measures nimget against aria2c on a local server that caps
// each connection's bandwidth, as CDNs do, and checks the M1 performance gate
// in DESIGN.md section 8:
//
//	throughput >= 95% of aria2c, < 1 CPU core at 1 Gbps, RSS < 100 MB
//
// Usage: go build -o nimget ./cmd/nimget && go run ./tools/bench -nimget ./nimget -gate
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
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
	nimget := flag.String("nimget", "./nimget", "path to the nimget binary")
	aria := flag.String("aria2c", "aria2c", "path to aria2c; skipped when missing")
	sizeMiB := flag.Int("size", 256, "throttled file size in MiB")
	rawMiB := flag.Int("raw-size", 512, "unthrottled file size in MiB")
	rate := flag.Float64("rate", 4, "per-connection cap in MiB/s")
	conns := flag.Int("connections", 16, "connections for both tools")
	gate := flag.Bool("gate", false, "exit non-zero when the gate fails")
	serveSize := flag.Int("serve", 0, "internal: serve this many MiB and print the URL")
	flag.Parse()
	if *serveSize > 0 {
		src := serve(randomData(*serveSize<<20), *rate*(1<<20))
		fmt.Printf("%s %x\n", src.url, src.sum)
		_, _ = io.Copy(io.Discard, os.Stdin) // exit with the parent, however it ends
		return
	}

	dir, err := os.MkdirTemp("", "nimget-bench-")
	check(err)
	defer os.RemoveAll(dir)

	// Servers run in child processes: on Linux a child's max RSS includes the
	// memory it shared with its parent before exec, so the parent stays small.
	throttled := spawnServer(*sizeMiB, *rate)
	unthrottled := spawnServer(*rawMiB, 0)

	// Each figure is the best of three runs, which keeps scheduler noise on
	// shared CI runners out of the 95% comparison.
	var rows []result
	nimgetThrottled := best(func() result { return runNimget(*nimget, throttled, dir, "nimget (capped)", *conns) })
	rows = append(rows, nimgetThrottled)
	var ariaThrottled *result
	if _, err := exec.LookPath(*aria); err == nil {
		r := best(func() result { return runAria(*aria, throttled, dir, "aria2c (capped)", *conns) })
		ariaThrottled = &r
		rows = append(rows, r)
	} else {
		fmt.Printf("aria2c not found (%s); skipping the comparison\n\n", *aria)
	}
	nimgetRaw := best(func() result { return runNimget(*nimget, unthrottled, dir, "nimget (uncapped)", *conns) })
	rows = append(rows, nimgetRaw)
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
	for _, r := range []result{nimgetThrottled, nimgetRaw} {
		if !r.correct {
			failures = append(failures, r.name+": output differs from the source")
		}
		if r.maxRSS >= 100e6 {
			failures = append(failures, fmt.Sprintf("%s: max RSS %.1f MB >= 100 MB", r.name, float64(r.maxRSS)/1e6))
		}
	}
	if c := nimgetRaw.coresAt1Gbps(); c >= 1 {
		failures = append(failures, fmt.Sprintf("nimget uses %.2f cores at 1 Gbps; limit is 1", c))
	}
	if ariaThrottled != nil {
		if ratio := nimgetThrottled.mbps() / ariaThrottled.mbps(); ratio < 0.95 {
			failures = append(failures, fmt.Sprintf("nimget throughput is %.0f%% of aria2c; gate is 95%%", ratio*100))
		} else {
			fmt.Printf("\nnimget throughput is %.0f%% of aria2c\n", ratio*100)
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
	size int64
	sum  [32]byte
}

// serverLifelines keeps the servers' stdin open; unreferenced, the pipes would
// be finalized and the servers would exit early.
var serverLifelines []io.WriteCloser

func spawnServer(sizeMiB int, rate float64) source {
	cmd := exec.Command(os.Args[0], "-serve", strconv.Itoa(sizeMiB), "-rate", strconv.FormatFloat(rate, 'f', -1, 64))
	stdout, err := cmd.StdoutPipe()
	check(err)
	stdin, err := cmd.StdinPipe()
	check(err)
	serverLifelines = append(serverLifelines, stdin) // closed only when this process exits
	check(cmd.Start())
	var url, sum string
	_, err = fmt.Fscan(stdout, &url, &sum)
	check(err)
	digest, err := hex.DecodeString(sum)
	check(err)
	src := source{url: url, size: int64(sizeMiB) << 20}
	copy(src.sum[:], digest)
	return src
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
	return source{url: "http://" + listener.Addr().String() + "/file.bin", size: int64(len(data)), sum: sha256.Sum256(data)}
}

func runNimget(bin string, src source, dir, name string, conns int) result {
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
	r := result{name: name, wall: time.Since(began), bytes: src.size}
	if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		r.cpu = time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
		r.maxRSS = int64(usage.Maxrss)
		if runtime.GOOS == "linux" {
			r.maxRSS *= 1024 // kilobytes on Linux, bytes on macOS
		}
	}
	r.correct = fileSum(out) == src.sum
	_ = os.Remove(out)
	return r
}

// fileSum streams the file: reading it whole would grow this process, and on
// Linux later children would inherit that RSS in their own figures.
func fileSum(path string) (sum [32]byte) {
	f, err := os.Open(path)
	if err != nil {
		return sum
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err == nil {
		copy(sum[:], h.Sum(nil))
	}
	return sum
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
