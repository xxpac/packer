package main

import (
	"fmt"
	"os"
)

const usageText = `packer - archive, compress, encrypt and split directories

Usage:
  packer <command> [flags] [args]

Commands:
  pack      archive dirs -> gzip -> [encrypt] -> [split]
  unpack    reverse of pack (auto-detects split/encryption/compression)
  encrypt   encrypt any file (passphrase-based AES-256-GCM)
  decrypt   decrypt a PACKENC file
  split     split any file into fixed-size parts
  merge     reassemble split parts

Run "packer <command> -h" for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "pack":
		err = cmdPack(args)
	case "unpack":
		err = cmdUnpack(args)
	case "encrypt":
		err = cmdEncrypt(args)
	case "decrypt":
		err = cmdDecrypt(args)
	case "split":
		err = cmdSplit(args)
	case "merge":
		err = cmdMerge(args)
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usageText)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "packer: error:", err)
		os.Exit(1)
	}
}
