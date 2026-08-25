package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"packer/internal/split"
)

func cmdSplit(args []string) error {
	fs := flag.NewFlagSet("split", flag.ContinueOnError)
	out := fs.String("o", "", "output base name (default: INPUT)")
	fs.StringVar(out, "output", "", "output base name")
	sizeStr := fs.String("split-size", "", "part size, e.g. 100MB (required)")
	fs.StringVar(sizeStr, "s", "", "part size (shorthand)")
	progMode := fs.String("progress", "auto", "progress reporting: auto|on|off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("split: expected exactly one input, got %d", len(rest))
	}
	input := rest[0]
	if *sizeStr == "" {
		return fmt.Errorf("split: --split-size is required")
	}
	size, err := split.ParseSize(*sizeStr)
	if err != nil {
		return err
	}
	if size <= 0 {
		return fmt.Errorf("split: --split-size must be > 0")
	}
	base := *out
	if base == "" {
		if input == "-" {
			return fmt.Errorf("split: -o is required when reading from stdin")
		}
		base = input
	}

	meter, err := newMeter(*progMode, false, "splitting", "split", false)
	if err != nil {
		return err
	}
	in, err := openInput(input)
	if err != nil {
		return err
	}
	defer in.Close()
	w := split.NewWriter(base, size)
	if _, err := io.Copy(w, meter.Reader(in)); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	meter.Finish()
	fmt.Fprintf(os.Stderr, "packer: wrote %d part(s): %v\n", len(w.Parts()), w.Parts())
	return nil
}

func cmdMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	out := fs.String("o", "", "output file (default: stem of the parts, or - for stdout)")
	fs.StringVar(out, "output", "", "output file")
	progMode := fs.String("progress", "auto", "progress reporting: auto|on|off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("merge: expected exactly one part or stem, got %d", len(rest))
	}
	input := rest[0]
	parts, err := split.FindParts(input)
	if err != nil {
		return err
	}
	outPath := *out
	if outPath == "" {
		outPath = split.Stem(input)
		if outPath == input {
			return fmt.Errorf("merge: cannot derive output name from %q; pass -o", input)
		}
	}

	meter, err := newMeter(*progMode, false, "merging", "merged", false)
	if err != nil {
		return err
	}
	rc, err := split.OpenParts(parts)
	if err != nil {
		return err
	}
	defer rc.Close()
	o, err := createOutput(outPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(meter.Writer(o), rc); err != nil {
		o.Close()
		return err
	}
	if err := o.Close(); err != nil {
		return err
	}
	meter.Finish()
	fmt.Fprintf(os.Stderr, "packer: merged %d part(s) into %s\n", len(parts), outPath)
	return nil
}
