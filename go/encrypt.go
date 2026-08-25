package main

import (
	"flag"
	"fmt"
	"strings"

	"packer/internal/crypto"
)

func cmdEncrypt(args []string) error {
	fs := flag.NewFlagSet("encrypt", flag.ContinueOnError)
	out := fs.String("o", "", "output file (default: INPUT.enc, or - for stdout)")
	fs.StringVar(out, "output", "", "output file")
	passFile := fs.String("passphrase-file", "", "read passphrase from file")
	progMode := fs.String("progress", "auto", "progress reporting: auto|on|off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("encrypt: expected exactly one input, got %d", len(rest))
	}
	input := rest[0]
	outPath := *out
	if outPath == "" {
		if input == "-" {
			outPath = "-"
		} else {
			outPath = input + ".enc"
		}
	}

	meter, err := newMeter(*progMode, false, "encrypting", "encrypted", false)
	if err != nil {
		return err
	}
	pass, err := resolvePassphrase(*passFile, true)
	if err != nil {
		return err
	}
	in, err := openInput(input)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := createOutput(outPath)
	if err != nil {
		return err
	}
	if err := crypto.Encrypt(o, meter.Reader(in), pass); err != nil {
		o.Close()
		return err
	}
	meter.Finish()
	return o.Close()
}

func cmdDecrypt(args []string) error {
	fs := flag.NewFlagSet("decrypt", flag.ContinueOnError)
	out := fs.String("o", "", "output file (default: strip .enc, or - for stdout)")
	fs.StringVar(out, "output", "", "output file")
	passFile := fs.String("passphrase-file", "", "read passphrase from file")
	progMode := fs.String("progress", "auto", "progress reporting: auto|on|off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("decrypt: expected exactly one input, got %d", len(rest))
	}
	input := rest[0]
	outPath := *out
	if outPath == "" {
		if input == "-" {
			outPath = "-"
		} else if strings.HasSuffix(input, ".enc") {
			outPath = strings.TrimSuffix(input, ".enc")
		} else {
			outPath = input + ".dec"
		}
	}

	meter, err := newMeter(*progMode, false, "decrypting", "decrypted", false)
	if err != nil {
		return err
	}
	pass, err := resolvePassphrase(*passFile, false)
	if err != nil {
		return err
	}
	in, err := openInput(input)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := createOutput(outPath)
	if err != nil {
		return err
	}
	if err := crypto.Decrypt(meter.Writer(o), in, pass); err != nil {
		o.Close()
		return err
	}
	meter.Finish()
	return o.Close()
}
