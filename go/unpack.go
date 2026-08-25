package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"

	"packer/internal/archive"
	"packer/internal/compress"
	"packer/internal/crypto"
	"packer/internal/detect"
	"packer/internal/split"
)

func cmdUnpack(args []string) error {
	fs := flag.NewFlagSet("unpack", flag.ContinueOnError)
	dest := fs.String("o", ".", "destination directory")
	fs.StringVar(dest, "output", ".", "destination directory")
	overwrite := fs.Bool("overwrite", false, "overwrite existing files")
	passFile := fs.String("passphrase-file", "", "read passphrase from file")
	progMode := fs.String("progress", "auto", "progress reporting: auto|on|off")
	verbose := fs.Bool("verbose", false, "list each first-level entry restored into the destination")
	fs.BoolVar(verbose, "v", false, "list each first-level entry (shorthand)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("unpack: expected exactly one input, got %d", len(rest))
	}
	input := rest[0]

	meter, err := newMeter(*progMode, *verbose, "unpacking", "unpacked", true)
	if err != nil {
		return err
	}

	rc, err := openArchiveInput(input)
	if err != nil {
		return err
	}
	defer rc.Close()

	var br *bufio.Reader = bufio.NewReader(rc)
	for {
		head, _ := br.Peek(len(crypto.Magic))
		if detect.IsPackenc(head) {
			pass, err := resolvePassphrase(*passFile, false)
			if err != nil {
				return err
			}
			pr, pw := io.Pipe()
			src := br
			go func() {
				err := crypto.Decrypt(pw, src, pass)
				pw.CloseWithError(err)
			}()
			br = bufio.NewReader(pr)
			continue
		}
		if detect.IsGzip(head) {
			gz, err := compress.NewReader(br)
			if err != nil {
				return err
			}
			br = bufio.NewReader(gz)
			continue
		}
		break
	}

	if err := archive.Extract(br, *dest, *overwrite, meter); err != nil {
		return err
	}
	// Drain any trailing bytes so background decrypt/gunzip goroutines finish.
	_, _ = io.Copy(io.Discard, br)
	meter.Finish()
	fmt.Fprintf(os.Stderr, "packer: extracted into %s\n", *dest)
	return nil
}

// openArchiveInput returns a reader for the input, merging split parts if present.
func openArchiveInput(input string) (io.ReadCloser, error) {
	if parts, err := split.FindParts(input); err == nil && len(parts) > 0 {
		return split.OpenParts(parts)
	}
	if fi, err := os.Stat(input); err == nil && !fi.IsDir() {
		return os.Open(input)
	}
	return nil, fmt.Errorf("cannot find input %q (no such file and no matching parts)", input)
}
