# packer

A CLI that packs a list of directories into a single compressed archive, with
optional passphrase encryption and optional splitting into fixed-size parts, and
reverses the whole process. Symbolic links are stored as links (never followed).
The encrypt/decrypt and split/merge stages are also standalone subcommands that
work on any file.

There are **two independent implementations** -- one in Go, one in Python --
that are **byte-compatible**: an archive (or encrypted file, or split set)
produced by one can be read by the other. A single [SPEC.md](SPEC.md) defines the
on-disk formats and behavior, which makes the head-to-head comparison meaningful.

## Layout

```
SPEC.md              format & behavior spec (source of truth for both impls)
go/                  Go implementation (single static, dependency-free binary)
py/                  Python implementation (uv project: `uv run packer`)
tests/conformance/   within-impl + cross-impl round trips + filter parity table
bench/               time / memory / size comparison harness
```

## Build & run

Go (no third-party modules; builds offline):

```bash
cd go && go build -o packer .
./packer --help
```

Python is managed with [uv](https://docs.astral.sh/uv/). The only third-party
dep is `cryptography` (for AES-GCM); the KDF uses stdlib `hashlib.scrypt`:

```bash
cd py
uv run packer --help        # uv builds the venv from pyproject.toml + uv.lock on first use
# or provision the environment explicitly, then run it:
uv sync                     # installs cryptography (version pinned in uv.lock)
uv run packer --help
```

No-install source run (handy for the offline test harness), or a plain-pip fallback:

```bash
PYTHONPATH=py python3 -m packer --help      # needs cryptography importable
pip install -r py/requirements.txt          # pip alternative to `uv sync`
```

## Commands

```bash
# Pack: archive + gzip + [encrypt] + [split]
packer pack -o backup ./project /etc/nginx \
  --encrypt \                       # prompt for passphrase (no echo, confirmed)
  --exclude '*.log' --exclude 'node_modules/' \
  --include '*.go' \                # allowlist (optional)
  --split-size 100MB                # -> backup.001, backup.002, ...

# Unpack: auto-detects split / encryption / compression
packer unpack backup.001 -o ./restore

# Standalone building blocks (any file)
packer encrypt -o secret.enc report.pdf
packer decrypt -o report.pdf secret.enc
packer split --split-size 100MB -o big.part big.iso   # big.part.001, ...
packer merge -o big.iso big.part.001
```

Because each layer is self-describing, the pieces compose freely and across
languages, e.g. `packer encrypt -o a.enc a.tgz && packer split -s 50MB -o a.part a.enc`.

### Common flags

- `-o, --output` output path/base name.
- Passphrase resolution order: `--passphrase-file FILE`, then the
  `PACKER_PASSPHRASE` environment variable, then an interactive no-echo prompt.
- `--progress auto|on|off` (all commands): live status line on stderr with the
  running byte count, throughput and (for `pack`/`unpack`) a file count, ending
  in a one-line summary. `auto` (default) shows it only when stderr is a
  terminal, so scripts and pipes stay quiet; use `on`/`off` to force it. See
  [SPEC.md](SPEC.md) section 7.1.
- `-v/--verbose` (`pack`/`unpack`): list entries on stderr as they are handled.
  `pack` prints each top-level entry inside every target dir -- a file as its
  path, a directory as its path plus its subtree's `(N files, SIZE)`. `unpack`
  prints each first-level entry restored under the destination. Works with or
  without `--progress` (see [SPEC.md](SPEC.md) section 7.2). Example:

```console
$ packer pack -v -o backup ./project /etc/nginx
packer: ./project/README.md
packer: ./project/src (128 files, 4.2 MiB)
packer: /etc/nginx/nginx.conf
packer: /etc/nginx/conf.d (7 files, 24.5 KiB)
packer: packed 137 files, 4.3 MiB in 0.4s (10.8 MiB/s)
packer: wrote backup
```

- `pack` adds: `-e/--encrypt`, `--split-size` (`100MB`, `1GiB`, `512KiB`, ...),
  `--level` (gzip 0-9), `--skip-symlinks`, and the filter flags below.
- `unpack` adds: `-o` destination dir (default `.`) and `--overwrite`.

## Filtering (gitignore-style, two-set)

`pack` selects files with two independent sets of gitignore-style patterns:

```
keep = (no --include patterns OR path matches an include) AND (path matches no --exclude)
```

Exclude wins on conflict, and an excluded directory prunes its whole subtree.
Patterns support `#` comments, leading `/` anchoring, trailing `/` (directories
only), `*`, `?`, `**`, `[...]` classes, and leading `!` negation. Provide them
with `--include`/`--exclude` (repeatable) or `--include-from`/`--exclude-from`
files. See [SPEC.md](SPEC.md) section 6 for exact semantics.

Pass `--names` when your lists name top-level entries rather than paths. Every
line is then matched against the first path component only, so a directory name
covers its whole subtree in either direction — no `/` prefix or `/**` suffix to
get right:

```bash
# pack only these top-level entries, src/ and docs/ recursively
printf 'src\ndocs\nREADME.md\n' > keep.txt
packer pack --names --include-from keep.txt -o out.pk .

# pack everything except these two directories and their contents
packer pack --names --exclude node_modules --exclude .git -o out.pk .
```

Lines are shell globs, so `*.log`, `[ab]cd` and `?cd` all work, but they only
ever match top-level names: `--names --exclude build` drops `./build` while
keeping `./src/build`, and `--names --exclude '*.log'` drops top-level logs
without touching `./sub/app.log`.

Because a name list enumerates things you expect to exist, `pack` warns about
any line that selected nothing, which catches typos and stale entries:

```
packer: warning: include name "docs" matched nothing
```

A line containing `/` is rejected outright rather than silently matching
nothing. See [SPEC.md](SPEC.md) sections 6.4 and 6.5.

## Cross-compatibility & formats

The container is a stack of self-describing layers so `unpack` needs no flags:

- gzip (magic `1f 8b`), written with mtime zeroed for reproducibility.
- encryption `PACKENC` (magic `PACKENC\x01` + JSON header): scrypt KDF
  (`N=2^15, r=8, p=1`) to a 256-bit key, then chunked AES-256-GCM (64 KiB chunks,
  per-chunk counter nonce with a final-chunk flag for truncation protection).
- split parts named `<base>.NNN`, merged in numeric order (plain byte slices).

`unpack` sniffs magic bytes at each stage, so it transparently merges, decrypts,
decompresses, and untars regardless of which implementation produced the input.

## Testing

```bash
# Go unit tests (scrypt RFC 7914 vectors, AEAD round trips, filter parity)
cd go && go test ./...

# Full conformance: within-impl + cross-impl (Go<->Python) round trips,
# plus the shared filter parity table.
bash tests/conformance/run.sh
```

The conformance suite verifies restored trees match on paths, contents,
permissions and symlink targets (encrypted output is intentionally
non-deterministic due to random salt/nonce, so equality is checked on the
restored tree rather than raw bytes).

## Benchmark

```bash
bash bench/run.sh 64        # dataset size in MB (default 64)
```

It reports pack/unpack wall time, peak RSS, and output size for both
implementations across plain / encrypt / split. Example (32 MB dataset; absolute
numbers vary by machine):

```
impl    mode               pack_s   unpack_s     out_size     pack_rss   unpack_rss
go      plain                0.66       0.04         17MB       6512KB       5124KB
go      encrypt              0.62       0.23         17MB      58256KB      55888KB
go      split                0.71       0.04         17MB       6676KB       5020KB
py      plain                0.53       0.15         17MB      22152KB      22108KB
py      encrypt              0.64       0.22         17MB      54220KB      54220KB
py      split                0.55       0.13         17MB      21816KB      22024KB
```

How to read it: output size is essentially identical (same gzip + tar format).
Go generally uses less baseline memory; the `encrypt` rows are dominated by
scrypt's ~32 MB working set on both sides. Times are close on this workload
since both are I/O and zlib bound.

## Implementation notes

- The Go build is intentionally **dependency-free** (stdlib `flag` for the CLI,
  a hand-rolled scrypt validated against the RFC 7914 test vectors, and a
  best-effort `stty` no-echo prompt), so it compiles with no network access.
- Extraction is hardened against path traversal ("Zip Slip") and against writing
  through symlinks, in both implementations.
