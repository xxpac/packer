package compress

import (
	"compress/gzip"
	"io"
	"time"
)

// DefaultLevel matches gzip's default compression level.
const DefaultLevel = gzip.DefaultCompression

// NewWriter returns a gzip writer configured for reproducible output: the
// modification-time field is zeroed and the OS byte is "unknown" (255), so the
// Go and Python implementations produce comparable headers.
func NewWriter(w io.Writer, level int) (*gzip.Writer, error) {
	zw, err := gzip.NewWriterLevel(w, level)
	if err != nil {
		return nil, err
	}
	zw.ModTime = time.Time{}
	zw.OS = 255
	return zw, nil
}

// NewReader wraps r as a gzip reader.
func NewReader(r io.Reader) (*gzip.Reader, error) {
	return gzip.NewReader(r)
}
