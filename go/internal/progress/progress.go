// Package progress renders lightweight, throttled progress to stderr while the
// long-running commands (pack/unpack/encrypt/decrypt/split/merge) do their work.
//
// A disabled meter (and a nil *Meter) is a cheap no-op, so callers can always
// construct one and pass it around without branching. Reader/Writer return the
// wrapped stream unchanged when disabled, so there is zero overhead off the
// happy path.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Enabled resolves the --progress flag value to whether live progress is shown.
// "auto" (or "") turns progress on only when stderr is a terminal, so piped or
// redirected runs (tests, benchmarks, `-o -`) stay quiet by default.
func Enabled(mode string) (bool, error) {
	switch mode {
	case "", "auto":
		return isTerminal(os.Stderr), nil
	case "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid --progress %q (want auto, on, or off)", mode)
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Meter is a throttled progress reporter.
//
// It is intended to be driven by a single goroutine (the one moving the bytes);
// Finish may be called from another goroutine only once that worker has
// provably finished (e.g. after its pipe reports EOF), which is how the Go pack
// pipeline uses it.
type Meter struct {
	ing, ed    string
	enabled    bool
	verbose    bool
	countFiles bool
	interval   time.Duration
	start      time.Time
	last       time.Time
	files      int64
	bytes      int64
	lastWidth  int
	out        io.Writer
}

// New returns a progress meter. ing/ed are the present ("packing") and past
// ("packed") tense verbs shown in the live line and final summary. countFiles
// controls whether a file count is displayed alongside the byte total. When
// verbose is set, per-entry lines (LogFile/LogDir) are emitted and byte/file
// counting stays on even if the live meter (enabled) is off.
func New(ing, ed string, enabled, countFiles, verbose bool) *Meter {
	return &Meter{
		ing:        ing,
		ed:         ed,
		enabled:    enabled,
		verbose:    verbose,
		countFiles: countFiles,
		interval:   100 * time.Millisecond,
		start:      time.Now(),
		out:        os.Stderr,
	}
}

// active reports whether the meter is doing anything (drawing a live line or
// emitting verbose lines); counting only happens while active.
func (m *Meter) active() bool { return m != nil && (m.enabled || m.verbose) }

// Verbose reports whether per-entry logging is on.
func (m *Meter) Verbose() bool { return m != nil && m.verbose }

// Snapshot returns the current (files, bytes) counters, for computing the
// per-subtree deltas shown by LogDir.
func (m *Meter) Snapshot() (int64, int64) {
	if m == nil {
		return 0, 0
	}
	return m.files, m.bytes
}

// AddFile increments the file counter (and may redraw).
func (m *Meter) AddFile() {
	if !m.active() {
		return
	}
	m.files++
	if m.enabled {
		m.draw(false)
	}
}

// AddBytes records n processed bytes (and may redraw).
func (m *Meter) AddBytes(n int64) {
	if !m.active() {
		return
	}
	m.bytes += n
	if m.enabled {
		m.draw(false)
	}
}

// Reader wraps r so bytes read through it are counted. Returns r unchanged when
// the meter is inactive.
func (m *Meter) Reader(r io.Reader) io.Reader {
	if !m.active() {
		return r
	}
	return &meterReader{m: m, r: r}
}

// Writer wraps w so bytes written through it are counted. Returns w unchanged
// when the meter is inactive.
func (m *Meter) Writer(w io.Writer) io.Writer {
	if !m.active() {
		return w
	}
	return &meterWriter{m: m, w: w}
}

// LogFile prints a permanent one-line entry for path (a top-level file). It is a
// no-op unless verbose is set.
func (m *Meter) LogFile(path string) {
	if m == nil || !m.verbose {
		return
	}
	m.logLine("packer: " + path)
}

// LogDir prints a permanent line for a top-level directory plus its subtree
// progress (file count and byte total). No-op unless verbose is set.
func (m *Meter) LogDir(path string, files, bytes int64) {
	if m == nil || !m.verbose {
		return
	}
	m.logLine(fmt.Sprintf("packer: %s (%d files, %s)", path, files, human(bytes)))
}

// logLine erases the transient live line (if any) and prints s on its own line,
// so verbose output and the in-place meter can coexist.
func (m *Meter) logLine(s string) {
	if m.lastWidth > 0 {
		fmt.Fprint(m.out, "\r"+pad(m.lastWidth)+"\r")
		m.lastWidth = 0
	}
	fmt.Fprintln(m.out, s)
}

func (m *Meter) draw(force bool) {
	now := time.Now()
	if !force && now.Sub(m.last) < m.interval {
		return
	}
	m.last = now
	line := "packer: " + m.ing + "  " + m.stats(now)
	fmt.Fprint(m.out, "\r"+line+pad(m.lastWidth-len(line)))
	m.lastWidth = len(line)
}

func (m *Meter) stats(now time.Time) string {
	rate := int64(0)
	if elapsed := now.Sub(m.start).Seconds(); elapsed > 0 {
		rate = int64(float64(m.bytes) / elapsed)
	}
	if m.countFiles {
		return fmt.Sprintf("%d files  %s  %s/s", m.files, human(m.bytes), human(rate))
	}
	return fmt.Sprintf("%s  %s/s", human(m.bytes), human(rate))
}

// Finish overwrites the transient line with a permanent one-line summary. It is
// safe (a no-op) when the meter is inactive.
func (m *Meter) Finish() {
	if !m.active() {
		return
	}
	now := time.Now()
	elapsed := now.Sub(m.start).Seconds()
	rate := int64(0)
	if elapsed > 0 {
		rate = int64(float64(m.bytes) / elapsed)
	}
	var summary string
	if m.countFiles {
		summary = fmt.Sprintf("packer: %s %d files, %s in %.1fs (%s/s)", m.ed, m.files, human(m.bytes), elapsed, human(rate))
	} else {
		summary = fmt.Sprintf("packer: %s %s in %.1fs (%s/s)", m.ed, human(m.bytes), elapsed, human(rate))
	}
	fmt.Fprint(m.out, "\r"+summary+pad(m.lastWidth-len(summary))+"\n")
	m.lastWidth = 0
}

func pad(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

type meterReader struct {
	m *Meter
	r io.Reader
}

func (mr *meterReader) Read(p []byte) (int, error) {
	n, err := mr.r.Read(p)
	if n > 0 {
		mr.m.AddBytes(int64(n))
	}
	return n, err
}

type meterWriter struct {
	m *Meter
	w io.Writer
}

func (mw *meterWriter) Write(p []byte) (int, error) {
	n, err := mw.w.Write(p)
	if n > 0 {
		mw.m.AddBytes(int64(n))
	}
	return n, err
}

// human formats a byte count with IEC binary units (matching py/packer/progress.py).
func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	i := -1
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
