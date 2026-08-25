"""Command-line interface (argparse). Mirrors the Go CLI in ../go (see SPEC.md 7)."""

import argparse
import getpass
import os
import sys

from . import archive, compress, split
from .crypto import DecryptReader, EncryptWriter, MAGIC
from .detect import Peekable, is_gzip, is_packenc
from .filter import Filter
from .progress import Progress, enabled as progress_enabled


def _make_progress(args, ing, ed, count_files=False):
    return Progress(ing, ed, progress_enabled(args.progress), count_files=count_files,
                    verbose=getattr(args, "verbose", False))


def resolve_passphrase(passfile, confirm):
    if passfile:
        with open(passfile, "rb") as f:
            return f.read().rstrip(b"\r\n")
    env = os.environ.get("PACKER_PASSPHRASE")
    if env is not None:
        return env.encode()
    p1 = getpass.getpass("Passphrase: ").encode()
    if not p1:
        raise ValueError("empty passphrase")
    if confirm:
        p2 = getpass.getpass("Confirm passphrase: ").encode()
        if p1 != p2:
            raise ValueError("passphrases do not match")
    return p1


def _open_input(path):
    if path == "-":
        return sys.stdin.buffer, False
    return open(path, "rb"), True


def _create_output(path):
    if path == "-":
        return sys.stdout.buffer, False
    return open(path, "wb"), True


def _read_lines(path):
    with open(path, "r") as f:
        return f.read().split("\n")


def _build_filter(args):
    inc = list(args.include or [])
    for p in (args.include_from or []):
        inc += _read_lines(p)
    exc = list(args.exclude or [])
    for p in (args.exclude_from or []):
        exc += _read_lines(p)
    return Filter(inc, exc)


def cmd_pack(args):
    for d in args.dirs:
        if not os.path.isdir(d):
            raise ValueError("%r is not a directory" % d)
    flt = _build_filter(args)
    prog = _make_progress(args, "packing", "packed", count_files=True)

    passphrase = None
    if args.encrypt:
        passphrase = resolve_passphrase(args.passphrase_file, confirm=True)

    if args.split_size:
        size = split.parse_size(args.split_size)
        if size <= 0:
            raise ValueError("--split-size must be > 0")
        sink = split.SplitWriter(args.output, size)
    else:
        sink = open(args.output, "wb")

    top = EncryptWriter(sink, passphrase) if args.encrypt else sink
    gz = compress.gzip_writer(top, args.level)
    archive.create(gz, args.dirs, skip_symlinks=args.skip_symlinks, flt=flt, progress=prog)
    gz.close()
    if args.encrypt:
        top.close()
    else:
        sink.close()
    prog.finish()

    if isinstance(sink, split.SplitWriter):
        sys.stderr.write("packer: wrote %d part(s): %s\n" % (len(sink.parts), sink.parts))
    else:
        sys.stderr.write("packer: wrote %s\n" % args.output)


def _open_archive_input(path):
    try:
        parts = split.find_parts(path)
        return split.PartsReader(parts)
    except FileNotFoundError:
        pass
    if os.path.isfile(path):
        return open(path, "rb")
    raise ValueError("cannot find input %r (no such file and no matching parts)" % path)


def cmd_unpack(args):
    prog = _make_progress(args, "unpacking", "unpacked", count_files=True)
    reader = _open_archive_input(args.input)
    pk = Peekable(reader)
    while True:
        head = pk.peek(len(MAGIC))
        if is_packenc(head):
            passphrase = resolve_passphrase(args.passphrase_file, confirm=False)
            pk = Peekable(DecryptReader(pk, passphrase))
            continue
        if is_gzip(head):
            pk = Peekable(compress.gzip_reader(pk))
            continue
        break
    archive.extract(pk, args.output, overwrite=args.overwrite, progress=prog)
    prog.finish()
    sys.stderr.write("packer: extracted into %s\n" % args.output)


def cmd_encrypt(args):
    input_ = args.input
    out = args.output
    if not out:
        out = "-" if input_ == "-" else input_ + ".enc"
    passphrase = resolve_passphrase(args.passphrase_file, confirm=True)
    prog = _make_progress(args, "encrypting", "encrypted")
    in_f, close_in = _open_input(input_)
    out_f, close_out = _create_output(out)
    try:
        top = EncryptWriter(_wrap_nonclosing(out_f), passphrase)
        _copy(in_f, top, progress=prog)
        top.close()
        prog.finish()
    finally:
        if close_in:
            in_f.close()
        if close_out:
            out_f.close()


def cmd_decrypt(args):
    input_ = args.input
    out = args.output
    if not out:
        if input_ == "-":
            out = "-"
        elif input_.endswith(".enc"):
            out = input_[: -len(".enc")]
        else:
            out = input_ + ".dec"
    passphrase = resolve_passphrase(args.passphrase_file, confirm=False)
    prog = _make_progress(args, "decrypting", "decrypted")
    in_f, close_in = _open_input(input_)
    out_f, close_out = _create_output(out)
    try:
        dr = DecryptReader(in_f, passphrase)
        _copy(dr, out_f, progress=prog)
        prog.finish()
    finally:
        if close_in:
            in_f.close()
        if close_out:
            out_f.close()


def cmd_split(args):
    if not args.split_size:
        raise ValueError("--split-size is required")
    size = split.parse_size(args.split_size)
    if size <= 0:
        raise ValueError("--split-size must be > 0")
    base = args.output
    if not base:
        if args.input == "-":
            raise ValueError("-o is required when reading from stdin")
        base = args.input
    prog = _make_progress(args, "splitting", "split")
    in_f, close_in = _open_input(args.input)
    w = split.SplitWriter(base, size)
    try:
        _copy(in_f, w, progress=prog)
        w.close()
        prog.finish()
    finally:
        if close_in:
            in_f.close()
    sys.stderr.write("packer: wrote %d part(s): %s\n" % (len(w.parts), w.parts))


def cmd_merge(args):
    parts = split.find_parts(args.input)
    out = args.output
    if not out:
        out = split.stem(args.input)
        if out == args.input:
            raise ValueError("cannot derive output name from %r; pass -o" % args.input)
    prog = _make_progress(args, "merging", "merged")
    reader = split.PartsReader(parts)
    out_f, close_out = _create_output(out)
    try:
        _copy(reader, out_f, progress=prog)
        prog.finish()
    finally:
        reader.close()
        if close_out:
            out_f.close()
    sys.stderr.write("packer: merged %d part(s) into %s\n" % (len(parts), out))


def _copy(src, dst, bufsize=1 << 16, progress=None):
    while True:
        chunk = src.read(bufsize)
        if not chunk:
            break
        dst.write(chunk)
        if progress is not None:
            progress.add_bytes(len(chunk))


class _wrap_nonclosing:
    def __init__(self, w):
        self.w = w

    def write(self, b):
        return self.w.write(b)

    def close(self):
        pass


def build_parser():
    p = argparse.ArgumentParser(prog="packer", description="archive, compress, encrypt and split directories")
    sub = p.add_subparsers(dest="command", required=True)

    def add_pass(sp):
        sp.add_argument("--passphrase-file", dest="passphrase_file", default="")

    def add_progress(sp):
        sp.add_argument("--progress", choices=["auto", "on", "off"], default="auto",
                        help="progress reporting (default: auto = on when stderr is a TTY)")

    def add_verbose(sp, helptext):
        sp.add_argument("-v", "--verbose", action="store_true", help=helptext)

    sp = sub.add_parser("pack", help="archive dirs -> gzip -> [encrypt] -> [split]")
    sp.add_argument("-o", "--output", default="archive.pk")
    sp.add_argument("-e", "--encrypt", action="store_true")
    sp.add_argument("--split-size", dest="split_size", default="")
    sp.add_argument("--level", type=int, default=compress.DEFAULT_LEVEL)
    sp.add_argument("--skip-symlinks", dest="skip_symlinks", action="store_true")
    sp.add_argument("--include", action="append", default=[])
    sp.add_argument("--exclude", action="append", default=[])
    sp.add_argument("--include-from", dest="include_from", action="append", default=[])
    sp.add_argument("--exclude-from", dest="exclude_from", action="append", default=[])
    add_pass(sp)
    add_progress(sp)
    add_verbose(sp, "list each top-level entry (dirs show their subtree progress)")
    sp.add_argument("dirs", nargs="+")
    sp.set_defaults(func=cmd_pack)

    sp = sub.add_parser("unpack", help="reverse of pack (auto-detects layers)")
    sp.add_argument("-o", "--output", default=".")
    sp.add_argument("--overwrite", action="store_true")
    add_pass(sp)
    add_progress(sp)
    add_verbose(sp, "list each first-level entry restored into the destination")
    sp.add_argument("input")
    sp.set_defaults(func=cmd_unpack)

    sp = sub.add_parser("encrypt", help="encrypt any file (AES-256-GCM)")
    sp.add_argument("-o", "--output", default="")
    add_pass(sp)
    add_progress(sp)
    sp.add_argument("input")
    sp.set_defaults(func=cmd_encrypt)

    sp = sub.add_parser("decrypt", help="decrypt a PACKENC file")
    sp.add_argument("-o", "--output", default="")
    add_pass(sp)
    add_progress(sp)
    sp.add_argument("input")
    sp.set_defaults(func=cmd_decrypt)

    sp = sub.add_parser("split", help="split any file into fixed-size parts")
    sp.add_argument("-o", "--output", default="")
    sp.add_argument("-s", "--split-size", dest="split_size", default="")
    add_progress(sp)
    sp.add_argument("input")
    sp.set_defaults(func=cmd_split)

    sp = sub.add_parser("merge", help="reassemble split parts")
    sp.add_argument("-o", "--output", default="")
    add_progress(sp)
    sp.add_argument("input")
    sp.set_defaults(func=cmd_merge)

    return p


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        args.func(args)
    except (ValueError, OSError) as e:
        sys.stderr.write("packer: error: %s\n" % e)
        return 1
    return 0
