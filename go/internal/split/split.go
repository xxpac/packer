package split

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ParseSize parses a human-readable size (see SPEC.md section 5).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	up := strings.ToUpper(s)
	type unit struct {
		suffix string
		mult   int64
	}
	// Order matters: check longer suffixes first.
	units := []unit{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(up, u.suffix) {
			num := strings.TrimSpace(up[:len(up)-len(u.suffix)])
			v, err := strconv.ParseInt(num, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q: %w", s, err)
			}
			if v < 0 {
				return 0, fmt.Errorf("invalid size %q: negative", s)
			}
			return v * u.mult, nil
		}
	}
	v, err := strconv.ParseInt(up, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	return v, nil
}

// Writer splits everything written to it into sequential parts named
// "<base>.NNN" (NNN zero-padded to 3 digits), each at most size bytes.
type Writer struct {
	base    string
	size    int64
	part    int
	cur     *os.File
	written int64
	parts   []string
}

func NewWriter(base string, size int64) *Writer {
	return &Writer{base: base, size: size}
}

func (w *Writer) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if w.cur == nil || w.written >= w.size {
			if err := w.rotate(); err != nil {
				return total, err
			}
		}
		space := w.size - w.written
		take := int64(len(p))
		if take > space {
			take = space
		}
		n, err := w.cur.Write(p[:take])
		w.written += int64(n)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (w *Writer) rotate() error {
	if w.cur != nil {
		if err := w.cur.Close(); err != nil {
			return err
		}
	}
	w.part++
	name := fmt.Sprintf("%s.%03d", w.base, w.part)
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	w.cur = f
	w.written = 0
	w.parts = append(w.parts, name)
	return nil
}

// Close flushes the final part. An input with no bytes still yields one empty part.
func (w *Writer) Close() error {
	if w.cur == nil {
		if err := w.rotate(); err != nil {
			return err
		}
	}
	return w.cur.Close()
}

// Parts returns the list of files written so far.
func (w *Writer) Parts() []string { return w.parts }

var suffixRe = regexp.MustCompile(`^(.*)\.(\d+)$`)

// Stem strips a trailing ".NNN" part suffix, if present.
func Stem(arg string) string {
	if m := suffixRe.FindStringSubmatch(arg); m != nil {
		return m[1]
	}
	return arg
}

// FindParts locates and orders the part files for a given part path or stem.
func FindParts(arg string) ([]string, error) {
	stem := arg
	if m := suffixRe.FindStringSubmatch(arg); m != nil {
		stem = m[1]
	}
	dir := filepath.Dir(stem)
	base := filepath.Base(stem)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	partRe := regexp.MustCompile("^" + regexp.QuoteMeta(base) + `\.(\d+)$`)
	type pp struct {
		n    int
		path string
	}
	var found []pp
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := partRe.FindStringSubmatch(e.Name()); m != nil {
			num, _ := strconv.Atoi(m[1])
			found = append(found, pp{num, filepath.Join(dir, e.Name())})
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no parts found for %q", arg)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	paths := make([]string, len(found))
	for i, f := range found {
		paths[i] = f.path
	}
	return paths, nil
}

type multiReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (m *multiReadCloser) Close() error {
	var firstErr error
	for _, c := range m.closers {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// OpenParts returns a reader that streams the concatenation of the parts.
func OpenParts(parts []string) (io.ReadCloser, error) {
	readers := make([]io.Reader, 0, len(parts))
	closers := make([]io.Closer, 0, len(parts))
	for _, p := range parts {
		f, err := os.Open(p)
		if err != nil {
			for _, c := range closers {
				c.Close()
			}
			return nil, err
		}
		readers = append(readers, f)
		closers = append(closers, f)
	}
	return &multiReadCloser{Reader: io.MultiReader(readers...), closers: closers}, nil
}
