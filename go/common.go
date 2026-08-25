package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"packer/internal/filter"
	"packer/internal/progress"
)

// newMeter builds a progress meter from a --progress flag value ("auto"|"on"|"off")
// and a --verbose flag.
func newMeter(mode string, verbose bool, ing, ed string, countFiles bool) (*progress.Meter, error) {
	enabled, err := progress.Enabled(mode)
	if err != nil {
		return nil, err
	}
	return progress.New(ing, ed, enabled, countFiles, verbose), nil
}

// stringSlice is a repeatable string flag.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func openInput(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

func createOutput(path string) (io.WriteCloser, error) {
	if path == "-" {
		return nopWriteCloser{os.Stdout}, nil
	}
	return os.Create(path)
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

func buildFilter(includes, excludes, includeFrom, excludeFrom []string) (*filter.Filter, error) {
	incLines := append([]string{}, includes...)
	for _, f := range includeFrom {
		lines, err := readLines(f)
		if err != nil {
			return nil, err
		}
		incLines = append(incLines, lines...)
	}
	excLines := append([]string{}, excludes...)
	for _, f := range excludeFrom {
		lines, err := readLines(f)
		if err != nil {
			return nil, err
		}
		excLines = append(excLines, lines...)
	}
	inc, err := filter.Compile(incLines)
	if err != nil {
		return nil, err
	}
	exc, err := filter.Compile(excLines)
	if err != nil {
		return nil, err
	}
	return &filter.Filter{Include: inc, Exclude: exc}, nil
}

// resolvePassphrase resolves the passphrase from a file, the environment, or an
// interactive no-echo prompt (confirmed twice when confirm is true).
func resolvePassphrase(passFile string, confirm bool) ([]byte, error) {
	if passFile != "" {
		data, err := os.ReadFile(passFile)
		if err != nil {
			return nil, err
		}
		return bytes.TrimRight(data, "\r\n"), nil
	}
	if env, ok := os.LookupEnv("PACKER_PASSPHRASE"); ok {
		return []byte(env), nil
	}
	fmt.Fprint(os.Stderr, "Passphrase: ")
	p1, err := readNoEcho()
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}
	if len(p1) == 0 {
		return nil, errors.New("empty passphrase")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Confirm passphrase: ")
		p2, err := readNoEcho()
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(p1, p2) {
			return nil, errors.New("passphrases do not match")
		}
	}
	return p1, nil
}

// readNoEcho reads a line from stdin with terminal echo disabled (best effort
// via stty, so no external Go modules are needed).
func readNoEcho() ([]byte, error) {
	toggle := func(arg string) {
		c := exec.Command("stty", arg)
		c.Stdin = os.Stdin
		_ = c.Run()
	}
	toggle("-echo")
	defer toggle("echo")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}
