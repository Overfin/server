package previews

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Format is how previews are made: as Jellyfin makes them by default.
type Format struct {
	Width      int // a thumbnail's, in pixels
	Columns    int // per sheet
	Rows       int
	IntervalMs int // one thumbnail every
	Quality    int // the sheets' JPEG quality
}

// Default is Jellyfin's own default: 320 pixels wide, every 10 seconds, on
// sheets of 10 x 10.
var Default = Format{Width: 320, Columns: 10, Rows: 10, IntervalMs: 10_000, Quality: 90}

// Maker makes a file's previews with ffmpeg.
type Maker struct {
	FFmpeg string
	// Hardware are the options that decode on the GPU the operator chose
	// (HardwareFor); without, or if that fails, the processor decodes.
	Hardware []string
	// Busy says whether something else needs the machine now (a stream
	// being transcoded): ffmpeg waits, stopped, until it doesn't.
	Busy func() bool
	// Every is how often Busy is asked.
	Every time.Duration
}

// HardwareFor are ffmpeg's options to decode on a GPU of mode ("nvenc",
// "qsv", "vaapi"; Overfin's gpu families), at device for those that have
// one; none for "none" or anything else.
func HardwareFor(mode, device string) []string {
	switch mode {
	case "nvenc":
		return []string{"-hwaccel", "cuda"}
	case "qsv", "vaapi":
		if strings.HasPrefix(device, "/dev/dri/") {
			return []string{"-hwaccel", "vaapi", "-hwaccel_device", device}
		}
	}
	return nil
}

// Make makes the previews of the file at path into the store, as fp's. It
// reads keyframes only (as Jellyfin does with "key frames only"), at a low
// priority, stopped while Busy.
func (m Maker) Make(ctx context.Context, s *Store, fp, path string, f Format) (Set, error) {
	made, err := s.making()
	if err != nil {
		return Set{}, err
	}
	frames := filepath.Join(made, "frames")
	if err := os.Mkdir(frames, 0o700); err != nil {
		os.RemoveAll(made)
		return Set{}, err
	}
	err = m.frames(ctx, path, frames, f, m.Hardware)
	if err != nil && len(m.Hardware) > 0 && ctx.Err() == nil {
		// The GPU couldn't: the processor, then.
		os.RemoveAll(frames)
		os.Mkdir(frames, 0o700)
		err = m.frames(ctx, path, frames, f, nil)
	}
	if err != nil {
		os.RemoveAll(made)
		return Set{}, err
	}
	set, err := tile(frames, made, f)
	os.RemoveAll(frames)
	if err == nil {
		err = s.put(fp, set, made)
	}
	if err != nil {
		os.RemoveAll(made)
		return Set{}, err
	}
	return set, nil
}

// frames has ffmpeg write one thumbnail every f.IntervalMs into dir.
func (m Maker) frames(ctx context.Context, path, dir string, f Format, hw []string) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "1"}
	args = append(args, hw...)
	args = append(args, "-skip_frame", "nokey", "-i", path, "-an", "-sn", "-dn", "-map", "0:v:0",
		"-vf", fmt.Sprintf("fps=1000/%d,scale=%d:-2", f.IntervalMs, f.Width), "-q:v", "4", filepath.Join(dir, "%06d.jpg"))
	cmd := exec.CommandContext(ctx, m.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	syscall.Setpriority(syscall.PRIO_PROCESS, pid, 19) // the lowest
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	every := m.Every
	if every <= 0 {
		every = 2 * time.Second
	}
	stopped := false
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				msg := strings.TrimSpace(stderr.String())
				if len(msg) > 300 {
					msg = msg[:300]
				}
				return fmt.Errorf("ffmpeg: %w: %s", err, strings.ReplaceAll(msg, path, "the file"))
			}
			return nil
		case <-tick.C:
			if m.Busy == nil {
				continue
			}
			busy := m.Busy()
			if busy && !stopped {
				syscall.Kill(pid, syscall.SIGSTOP)
				stopped = true
			} else if !busy && stopped {
				syscall.Kill(pid, syscall.SIGCONT)
				stopped = false
			}
		case <-ctx.Done():
			if stopped {
				syscall.Kill(pid, syscall.SIGCONT) // so it can end
			}
			return <-done
		}
	}
}

// tile puts the thumbnails in frames, in order, on sheets in dir.
func tile(frames, dir string, f Format) (Set, error) {
	entries, err := os.ReadDir(frames)
	if err != nil {
		return Set{}, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jpg") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return Set{}, errors.New("previews: no frame")
	}
	if len(names) > 100_000 {
		names = names[:100_000]
	}
	per := f.Columns * f.Rows
	var sheet *image.RGBA
	var height int
	write := func(n int) error {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, sheet, &jpeg.Options{Quality: f.Quality}); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, strconv.Itoa(n)+".jpg"), buf.Bytes(), 0o600)
	}
	for i, name := range names {
		img, err := readJPEG(filepath.Join(frames, name))
		if err != nil {
			return Set{}, err
		}
		if height == 0 {
			height = img.Bounds().Dy()
			if height <= 0 || height > 1024 {
				return Set{}, errors.New("previews: odd frames")
			}
		}
		at := i % per
		if at == 0 {
			// Every sheet is the whole grid, the last too (black where
			// it has no thumbnail): players scale a sheet as one.
			sheet = image.NewRGBA(image.Rect(0, 0, f.Width*f.Columns, height*f.Rows))
			draw.Draw(sheet, sheet.Bounds(), image.Black, image.Point{}, draw.Src)
		}
		x, y := (at%f.Columns)*f.Width, (at/f.Columns)*height
		draw.Draw(sheet, image.Rect(x, y, x+f.Width, y+height), img, img.Bounds().Min, draw.Src)
		if at == per-1 || i == len(names)-1 {
			if err := write(i / per); err != nil {
				return Set{}, err
			}
		}
	}
	return Set{Width: f.Width, Height: height, Columns: f.Columns, Rows: f.Rows, Count: len(names), IntervalMs: f.IntervalMs}, nil
}

func readJPEG(path string) (image.Image, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return jpeg.Decode(bytes.NewReader(b))
}
