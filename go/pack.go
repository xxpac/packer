package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"packer/internal/archive"
	"packer/internal/compress"
	"packer/internal/crypto"
	"packer/internal/split"
)

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	out := fs.String("o", "archive.pk", "output archive path (base name)")
	fs.StringVar(out, "output", "archive.pk", "output archive path (base name)")
	encrypt := fs.Bool("encrypt", false, "encrypt the archive")
	fs.BoolVar(encrypt, "e", false, "encrypt the archive (shorthand)")
	splitSize := fs.String("split-size", "", "split output into parts of this size (e.g. 100MB)")
	level := fs.Int("level", compress.DefaultLevel, "gzip compression level (-1..9)")
	skipSym := fs.Bool("skip-symlinks", false, "omit symlinks instead of storing them")
	passFile := fs.String("passphrase-file", "", "read passphrase from file")
	progMode := fs.String("progress", "auto", "progress reporting: auto|on|off")
	verbose := fs.Bool("verbose", false, "list each top-level entry (dirs show their subtree progress)")
	fs.BoolVar(verbose, "v", false, "list each top-level entry (shorthand)")
	var includes, excludes, includeFrom, excludeFrom stringSlice
	fs.Var(&includes, "include", "include pattern, gitignore-style (repeatable)")
	fs.Var(&excludes, "exclude", "exclude pattern, gitignore-style (repeatable)")
	fs.Var(&includeFrom, "include-from", "read include patterns from file (repeatable)")
	fs.Var(&excludeFrom, "exclude-from", "read exclude patterns from file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs := fs.Args()
	if len(dirs) == 0 {
		return errors.New("pack: need at least one input directory")
	}
	for _, d := range dirs {
		fi, err := os.Stat(d)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("pack: %q is not a directory", d)
		}
	}

	flt, err := buildFilter(includes, excludes, includeFrom, excludeFrom)
	if err != nil {
		return err
	}

	meter, err := newMeter(*progMode, *verbose, "packing", "packed", true)
	if err != nil {
		return err
	}

	var pass []byte
	if *encrypt {
		pass, err = resolvePassphrase(*passFile, true)
		if err != nil {
			return err
		}
	}

	var dst io.Writer
	var closeDst func() error
	var sw *split.Writer
	if *splitSize != "" {
		size, err := split.ParseSize(*splitSize)
		if err != nil {
			return err
		}
		if size <= 0 {
			return errors.New("pack: --split-size must be > 0")
		}
		sw = split.NewWriter(*out, size)
		dst = sw
		closeDst = sw.Close
	} else {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		dst = f
		closeDst = f.Close
	}

	pr, pw := io.Pipe()
	go func() {
		gz, err := compress.NewWriter(pw, *level)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		err = archive.Create(gz, dirs, archive.Options{SkipSymlinks: *skipSym, Filter: flt, Progress: meter})
		if cerr := gz.Close(); err == nil {
			err = cerr
		}
		pw.CloseWithError(err)
	}()

	var runErr error
	if *encrypt {
		runErr = crypto.Encrypt(dst, pr, pass)
	} else {
		_, runErr = io.Copy(dst, pr)
	}
	pr.Close()
	if cerr := closeDst(); runErr == nil {
		runErr = cerr
	}
	if runErr != nil {
		return runErr
	}
	meter.Finish()

	if sw != nil {
		fmt.Fprintf(os.Stderr, "packer: wrote %d part(s): %v\n", len(sw.Parts()), sw.Parts())
	} else {
		fmt.Fprintf(os.Stderr, "packer: wrote %s\n", *out)
	}
	return nil
}
