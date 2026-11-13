package images

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime/debug"
	"syscall"
	"time"
)

// Worker processes make images apart from the backend (Workers): each is
// the backend's own program, started with workerEnv set, run as a loop
// that reads a request on its standard input, makes the images
// (InProcess), and writes the answer on its standard output. A worker
// caps its own memory before anything else; the backend gives each image
// a time limit. A worker that crashes or runs over is replaced, and its
// image refused.

// rlimitData is Linux's RLIMIT_DATA.
const rlimitData = syscall.RLIMIT_DATA

// workerEnv marks a worker process.
const workerEnv = "OVERFIN_IMAGE_WORKER"

// Limits of one worker: the address space it may take (a decoded picture
// takes up to 64 MiB, its smaller sizes and encodings a little more;
// Go's runtime its own share), and how long one image may take.
const (
	workerMemory  = 1 << 30
	workerTimeout = 30 * time.Second
	maxRaw        = 64 << 20 // an image sent to a worker (jellyfin.MaxImageBytes and uploads are smaller)
	maxWidths     = 16
)

// RunWorker runs this process as an image worker when it was started as
// one, and says whether it was (the caller then exits).
func RunWorker() bool {
	if os.Getenv(workerEnv) == "" {
		return false
	}
	capMemory()
	if err := serveWorker(os.Stdin, os.Stdout); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "image worker:", err)
		os.Exit(1)
	}
	return true
}

// capMemory caps the worker's memory, first thing: past it, the worker
// fails, not the machine. RLIMIT_DATA, not RLIMIT_AS: Go's runtime reserves
// far more address space than it uses, and the data limit counts only the
// writable memory it actually maps.
func capMemory() {
	if !raceBuild {
		_ = syscall.Setrlimit(rlimitData, &syscall.Rlimit{Cur: workerMemory, Max: workerMemory})
	}
	debug.SetMemoryLimit(workerMemory / 2)
}

// A request: widths count (u32), each width (u32), content type length
// (u32) and bytes, raw length (u32) and bytes. An answer: status (u32:
// 0 made, 1 unsafe, 2 failed), the original's width (u32), outputs count
// (u32), each output's length (u32) and bytes.
const (
	statusMade = iota
	statusUnsafe
	statusFailed
)

func serveWorker(in io.Reader, out io.Writer) error {
	for {
		widths, contentType, raw, err := readRequest(in)
		if err != nil {
			return err
		}
		outs, width, err := InProcess(raw, widths, contentType)
		status := uint32(statusMade)
		switch {
		case errors.Is(err, ErrUnsafe):
			status, outs = statusUnsafe, nil
		case err != nil:
			status, outs = statusFailed, nil
		}
		if err := writeAnswer(out, status, width, outs); err != nil {
			return err
		}
	}
}

func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

func readBytes(r io.Reader, max uint32) ([]byte, error) {
	n, err := readU32(r)
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("%d bytes, more than %d", n, max)
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return b, err
}

func writeU32(w io.Writer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := w.Write(b[:])
	return err
}

func writeBytes(w io.Writer, b []byte) error {
	if err := writeU32(w, uint32(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readRequest(r io.Reader) ([]int, string, []byte, error) {
	n, err := readU32(r)
	if err != nil {
		return nil, "", nil, err
	}
	if n > maxWidths {
		return nil, "", nil, errors.New("too many widths")
	}
	widths := make([]int, n)
	for i := range widths {
		w, err := readU32(r)
		if err != nil {
			return nil, "", nil, err
		}
		widths[i] = int(w)
	}
	ct, err := readBytes(r, 64)
	if err != nil {
		return nil, "", nil, err
	}
	raw, err := readBytes(r, maxRaw)
	if err != nil {
		return nil, "", nil, err
	}
	return widths, string(ct), raw, nil
}

func writeRequest(w io.Writer, widths []int, contentType string, raw []byte) error {
	if err := writeU32(w, uint32(len(widths))); err != nil {
		return err
	}
	for _, v := range widths {
		if err := writeU32(w, uint32(v)); err != nil {
			return err
		}
	}
	if err := writeBytes(w, []byte(contentType)); err != nil {
		return err
	}
	return writeBytes(w, raw)
}

func writeAnswer(w io.Writer, status uint32, width int, outs [][]byte) error {
	for _, v := range []uint32{status, uint32(width), uint32(len(outs))} {
		if err := writeU32(w, v); err != nil {
			return err
		}
	}
	for _, o := range outs {
		if err := writeBytes(w, o); err != nil {
			return err
		}
	}
	return nil
}

func readAnswer(r io.Reader) ([][]byte, int, error) {
	status, err := readU32(r)
	if err != nil {
		return nil, 0, err
	}
	width, err := readU32(r)
	if err != nil {
		return nil, 0, err
	}
	n, err := readU32(r)
	if err != nil {
		return nil, 0, err
	}
	if n > maxWidths {
		return nil, 0, errors.New("too many outputs")
	}
	outs := make([][]byte, n)
	for i := range outs {
		if outs[i], err = readBytes(r, maxRaw); err != nil {
			return nil, 0, err
		}
	}
	switch status {
	case statusMade:
		return outs, int(width), nil
	case statusUnsafe:
		return nil, 0, ErrUnsafe
	default:
		return nil, 0, errors.New("images: the worker couldn't make the image")
	}
}

// worker is one running worker process.
type worker struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (w *worker) stop() {
	w.in.Close()
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
}

// Pool runs images through n worker processes, started on demand,
// replaced when one fails.
type Pool struct {
	program string
	logger  *slog.Logger
	idle    chan *worker
	slots   chan struct{}
	timeout time.Duration
}

// Workers makes a pool of n workers, each this program started again as a
// worker (its main calls RunWorker first). Install it with
// SetTransformer(pool.Transform).
func Workers(n int, logger *slog.Logger) (*Pool, error) {
	program, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Pool{program: program, logger: logger, idle: make(chan *worker, n), slots: make(chan struct{}, n), timeout: workerTimeout}, nil
}

func (p *Pool) start() (*worker, error) {
	cmd := exec.Command(p.program)
	cmd.Env = append(os.Environ(), workerEnv+"=1")
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &worker{cmd: cmd, in: in, out: out}, nil
}

// Transform makes images in a worker (a Transformer).
func (p *Pool) Transform(raw []byte, widths []int, contentType string) ([][]byte, int, error) {
	if len(raw) > maxRaw || len(widths) > maxWidths {
		return nil, 0, ErrUnsafe
	}
	p.slots <- struct{}{}
	defer func() { <-p.slots }()
	var w *worker
	select {
	case w = <-p.idle:
	default:
		var err error
		if w, err = p.start(); err != nil {
			return nil, 0, fmt.Errorf("images: starting a worker: %w", err)
		}
	}
	type result struct {
		outs  [][]byte
		width int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		if err := writeRequest(w.in, widths, contentType, raw); err != nil {
			done <- result{err: err}
			return
		}
		outs, width, err := readAnswer(w.out)
		done <- result{outs, width, err}
	}()
	select {
	case r := <-done:
		if r.err != nil && !errors.Is(r.err, ErrUnsafe) {
			// The worker crashed or answered nonsense: it goes, and the
			// image with it.
			w.stop()
			p.logger.Warn("an image worker failed; the image is refused", "err", r.err)
			return nil, 0, ErrUnsafe
		}
		p.idle <- w
		return r.outs, r.width, r.err
	case <-time.After(p.timeout):
		w.stop()
		p.logger.Warn("an image took too long in its worker; the image is refused")
		return nil, 0, ErrUnsafe
	}
}

// Close stops the idle workers.
func (p *Pool) Close() {
	for {
		select {
		case w := <-p.idle:
			w.stop()
		default:
			return
		}
	}
}
