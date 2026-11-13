// Command linkbench measures the link between a backend and a gateway on
// the real route between two machines, against plain TCP on the same
// route. It isn't part of Overfin's images.
//
// On the gateway's machine:
//
//	linkbench tcp-sink -listen :PORT                  (plain TCP, receives)
//	linkbench gateway -listen :PORT -code CODE [-streams 4 -mbps 100 -seconds 30]
//
// On the backend's machine:
//
//	linkbench tcp-source -addr HOST:PORT [-conns 4 -seconds 20]
//	linkbench backend -gateway HOST:PORT              (prints the code first)
//
// Data flows from the backend to the gateway, as video does.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/backend"
	"github.com/overfin/server/internal/link"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "linkbench tcp-sink|tcp-source|gateway|backend [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "tcp-sink":
		err = tcpSink(os.Args[2:])
	case "tcp-source":
		err = tcpSource(os.Args[2:])
	case "gateway":
		err = gateway(os.Args[2:])
	case "backend":
		err = runBackend(os.Args[2:])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

func mbps(bytes int64, d time.Duration) float64 { return float64(bytes) * 8 / d.Seconds() / 1e6 }

// tcpSink receives plain TCP connections and reports each one's rate and
// the total, for the baseline.
func tcpSink(args []string) error {
	fs := flag.NewFlagSet("tcp-sink", flag.ExitOnError)
	listen := fs.String("listen", ":9000", "address to listen on")
	fs.Parse(args)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	fmt.Println("tcp-sink listening on", *listen)
	var total atomic.Int64
	var first time.Time
	var mu sync.Mutex
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		mu.Lock()
		if first.IsZero() || time.Since(first) > time.Minute {
			first = time.Now()
			total.Store(0)
		}
		mu.Unlock()
		go func() {
			defer c.Close()
			start := time.Now()
			n, _ := io.Copy(io.Discard, c)
			d := time.Since(start)
			t := total.Add(n)
			mu.Lock()
			fmt.Printf("connection from %s: %.0f Mb/s over %.1fs (all so far: %.0f Mb/s)\n", c.RemoteAddr(), mbps(n, d), d.Seconds(), mbps(t, time.Since(first)))
			mu.Unlock()
		}()
	}
}

// tcpSource sends zeros on plain TCP connections for a while.
func tcpSource(args []string) error {
	fs := flag.NewFlagSet("tcp-source", flag.ExitOnError)
	addr := fs.String("addr", "", "the sink's host:port")
	conns := fs.Int("conns", 1, "connections at once")
	seconds := fs.Int("seconds", 20, "how long")
	fs.Parse(args)
	var wg sync.WaitGroup
	var total atomic.Int64
	start := time.Now()
	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", *addr)
			if err != nil {
				fmt.Println(err)
				return
			}
			defer c.Close()
			buf := make([]byte, 64<<10)
			end := time.Now().Add(time.Duration(*seconds) * time.Second)
			for time.Now().Before(end) {
				n, err := c.Write(buf)
				total.Add(int64(n))
				if err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("sent %.0f Mb/s over %d connection(s)\n", mbps(total.Load(), time.Since(start)), *conns)
	return nil
}

// runBackend pairs with the gateway (printing the code) and serves the
// backend's routes over the link until stopped.
func runBackend(args []string) error {
	fs := flag.NewFlagSet("backend", flag.ExitOnError)
	gw := fs.String("gateway", "", "the gateway's host:port")
	dir := fs.String("dir", "", "folder for the backend's key and pairing (default: a new temporary one)")
	fs.Parse(args)
	if *dir == "" {
		d, err := os.MkdirTemp("", "linkbench-backend-")
		if err != nil {
			return err
		}
		*dir = d
	}
	b, err := link.NewBackend(*dir, "linkbench", backend.Routes(), logger)
	if err != nil {
		return err
	}
	code, err := b.Pair(*gw, "")
	if err != nil {
		return err
	}
	fmt.Println("pairing code:", code)
	b.Run(context.Background())
	return nil
}

// gateway waits for the backend, then measures: all streams as fast as
// they go, then each stream at a video's bitrate with page requests
// alongside.
func gateway(args []string) error {
	fs := flag.NewFlagSet("gateway", flag.ExitOnError)
	listen := fs.String("listen", ":9443", "address to listen on")
	code := fs.String("code", "", "the backend's pairing code")
	dir := fs.String("dir", "", "folder for the gateway's key and pairing (default: a new temporary one)")
	streams := fs.Int("streams", 4, "streams at once")
	rate := fs.Float64("mbps", 100, "each stream's bitrate, in the paced run")
	seconds := fs.Int("seconds", 30, "how long each run lasts")
	fs.Parse(args)
	if *dir == "" {
		d, err := os.MkdirTemp("", "linkbench-gateway-")
		if err != nil {
			return err
		}
		*dir = d
	}
	g, err := link.NewGateway(*dir, "linkbench", func() string { return *code }, logger)
	if err != nil {
		return err
	}
	go func() {
		if err := g.Listen(context.Background(), *listen); err != nil {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()
	fmt.Println("waiting for the backend on", *listen)
	if err := g.Wait(context.Background()); err != nil {
		return err
	}
	client := g.Client()
	d := time.Duration(*seconds) * time.Second

	fmt.Printf("\n== %d streams, as fast as they go, %ds\n", *streams, *seconds)
	got := run(client, *streams, d, 0)
	var sum int64
	for i, n := range got {
		fmt.Printf("stream %d: %.0f Mb/s\n", i+1, mbps(n, d))
		sum += n
	}
	fmt.Printf("all: %.0f Mb/s\n", mbps(sum, d))

	fmt.Printf("\n== %d streams at %.0f Mb/s each, page requests alongside, %ds\n", *streams, *rate, *seconds)
	stop := make(chan struct{})
	lat := make(chan []time.Duration, 1)
	go func() { lat <- pages(client, stop) }()
	got = run(client, *streams, d, *rate)
	close(stop)
	worst := 100.0
	for i, n := range got {
		pct := mbps(n, d) / *rate * 100
		worst = min(worst, pct)
		fmt.Printf("stream %d: %.1f Mb/s (%.0f%% of its bitrate)\n", i+1, mbps(n, d), pct)
	}
	l := <-lat
	slices.Sort(l)
	if len(l) > 0 {
		fmt.Printf("page requests: %d, median %v, 95th %v, worst %v\n", len(l), l[len(l)/2].Round(time.Millisecond),
			l[len(l)*95/100].Round(time.Millisecond), l[len(l)-1].Round(time.Millisecond))
	}
	fmt.Printf("weakest stream: %.0f%% of its bitrate\n", worst)
	return nil
}

// run reads n streams for d; at rate Mb/s each when rate > 0 (reading no
// faster, as a player does), else as fast as they come. It gives each
// stream's bytes.
func run(client *http.Client, n int, d time.Duration, rate float64) []int64 {
	got := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), d)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://backend/link/v1/speed?bytes="+strconv.Itoa(10<<30), nil)
			resp, err := client.Do(req)
			if err != nil {
				fmt.Println("stream:", err)
				return
			}
			defer resp.Body.Close()
			buf := make([]byte, 64<<10)
			start := time.Now()
			for {
				k, err := resp.Body.Read(buf)
				got[i] += int64(k)
				if err != nil {
					return
				}
				if rate > 0 {
					// Ahead of the bitrate: wait until it catches up.
					due := time.Duration(float64(got[i]*8) / (rate * 1e6) * float64(time.Second))
					if ahead := due - time.Since(start); ahead > 0 {
						select {
						case <-time.After(ahead):
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	return got
}

// pages asks for the backend's status twice a second until stop, and
// gives each answer's time.
func pages(client *http.Client, stop <-chan struct{}) []time.Duration {
	var out []time.Duration
	for {
		select {
		case <-stop:
			return out
		case <-time.After(500 * time.Millisecond):
		}
		start := time.Now()
		resp, err := client.Get("https://backend/link/v1/status")
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		out = append(out, time.Since(start))
	}
}
