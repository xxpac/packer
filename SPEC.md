# Packer format & behavior specification

This document is the source of truth for both the Go (`go/`) and Python (`py/`)
implementations. Anything produced by one implementation MUST be consumable by
the other. Version: `1`.

## 1. Overview / pipeline

`pack` composes layers in this order:

```
input dirs --> filter --> tar --> gzip --> [encrypt] --> [split] --> output file(s)
```

`unpack` reverses them, auto-detecting each layer by inspecting magic bytes:

```
part files --> merge --> [decrypt] --> [gunzip] --> untar --> restored tree
```

Each layer is self-describing, so the standalone commands (`encrypt`/`decrypt`,
`split`/`merge`) operate on any byte stream and compose in any order.

## 2. Tar layer (archive)

- A single POSIX/ustar (or PAX where needed) tar stream contains every input dir.
- Rooting: each input directory argument `D` is stored under its base name.
  `rootName = base(abs(clean(D)))`. Example: `/var/log` -> entries `log/...`,
  `./project` -> `project/...`. If two inputs resolve to the same `rootName`,
  packing fails with an error.
- Entry names always use forward slashes and are relative (never absolute, never
  contain `.` or `..` components).
- Traversal does NOT follow symbolic links (uses lstat semantics). Symlinks are
  stored as symlink entries with the raw (unresolved) target as link name.
  With `--skip-symlinks`, symlinks are omitted entirely.
- Entry types stored: regular files, directories, symlinks. Other special files
  (fifo, socket, device) are skipped with a warning.
- Metadata: file mode (permission bits) and modification time are stored and
  restored. Uid/gid/user/group are not required to match on restore.

### Extraction safety (both implementations)

- Reject any entry whose cleaned path escapes the destination directory
  (absolute paths, `..` traversal).
- Do not write through symlinks: before creating an entry, the real path of its
  parent directory must remain inside the destination root.

## 3. Gzip layer (compression)

- Standard gzip (RFC 1952). The modification-time field MUST be written as `0`
  for reproducible output. Detected by the magic bytes `1f 8b`.
- Compression level is selectable at pack time (`--level`, default 6). The level
  does not affect decompressibility or cross-implementation compatibility.

## 4. Encryption layer (`PACKENC`)

Byte layout:

```
offset 0  : magic            = "PACKENC\x01"        (8 bytes)
offset 8  : header_len        = uint32 big-endian    (4 bytes)
offset 12 : header_json       = header_len bytes of UTF-8 JSON
then      : payload           = one or more encrypted chunks (see below)
```

Header JSON (field order irrelevant):

```json
{
  "version": 1,
  "kdf": "scrypt",
  "salt": "<base64-std>",          // 16 random bytes
  "scrypt": { "N": 32768, "r": 8, "p": 1 },
  "keylen": 32,
  "cipher": "aes-256-gcm",
  "nonce_prefix": "<base64-std>",  // 7 random bytes
  "chunk_size": 65536              // plaintext bytes per chunk
}
```

### 4.1 Key derivation

`key = scrypt(passphrase_utf8, salt, N, r, p, keylen)` per RFC 7914.
Defaults: `N = 2^15 = 32768`, `r = 8`, `p = 1`, `keylen = 32`.
- Go: hand-rolled scrypt (PBKDF2-HMAC-SHA256 + Salsa20/8 + BlockMix + ROMix),
  verified against RFC 7914 test vectors.
- Python: `hashlib.scrypt(passphrase, salt=salt, n=N, r=r, p=p, dklen=keylen,
  maxmem=2*128*r*N)`.

### 4.2 Streaming AEAD (chunked AES-256-GCM)

Plaintext is split into chunks of `chunk_size` bytes (the last chunk may be
smaller; an empty input still yields exactly one final chunk of length 0).

For chunk index `i` (0-based) the 12-byte GCM nonce is:

```
nonce = nonce_prefix (7 bytes) || counter (uint32 big-endian, 4 bytes) || last_flag (1 byte)
last_flag = 0x01 for the final chunk, else 0x00
```

On-disk each chunk is `AES-256-GCM.Seal(key, nonce, plaintext_chunk)` = ciphertext
followed by the 16-byte tag. No additional authenticated data (AAD is empty).

Decryption reads `chunk_size + 16` bytes per chunk; whether a chunk is final is
determined by look-ahead (EOF after this chunk => final). The decryptor computes
`last_flag` accordingly, so dropping/appending/reordering chunks makes GCM
authentication fail (truncation protection). A wrong passphrase also fails
authentication on the first chunk.

## 5. Split layer

- The input stream is written to sequentially numbered parts:
  `<output>.<NNN>` with `NNN` zero-padded to at least 3 digits, starting at `001`.
- Each part (except possibly the last) is exactly `--split-size` bytes. Parts are
  plain byte slices; concatenating them in numeric order reproduces the input
  exactly (no re-encoding).
- `merge` accepts any one part path (or the stem). It derives the stem by
  stripping a trailing `.<digits>` suffix, collects all siblings matching
  `stem.<digits>`, sorts them by numeric value, and concatenates.

### Size parsing (both implementations)

Case-insensitive suffixes on a non-negative integer:
- Decimal: `KB=1e3`, `MB=1e6`, `GB=1e9`, `TB=1e12`.
- Binary: `KiB=2^10`, `MiB=2^20`, `GiB=2^30`, `TiB=2^40`, and single letters
  `K/M/G/T` are treated as binary (`Ki/Mi/Gi/Ti`).
- No suffix (or `B`) = bytes.

## 6. Filtering (pack-time, gitignore-style, two-set)

Selects which paths (relative to each input's `rootName`, forward slashes) are
included. Directories are tested with a trailing `/`.

### 6.1 Pattern syntax

Compiled to a regular expression identically in both implementations:
- Blank lines and lines beginning with `#` are ignored.
- A leading `!` negates the line (re-includes within its own list).
- A leading `/` anchors the pattern to the root; a `/` anywhere except the last
  char also anchors it. Otherwise the pattern matches at any depth (basename).
- A trailing `/` makes the pattern match directories only.
- Wildcards: `*` matches any run of non-`/` characters; `?` matches one non-`/`
  character; `**` matches across `/` (`**/` prefix, `/**` suffix, `/**/` infix);
  `[...]` is a character class (with `!`/`^` negation and `a-z` ranges).

### 6.2 Two-set semantics

Given the compiled include set `I` and exclude set `E`:
```
included = (I is empty) OR I.matches(path)
excluded = E.matches(path)
keep     = included AND NOT excluded          # exclude wins ties
```
Pattern sources: `--include`/`--exclude` (repeatable) and
`--include-from FILE`/`--exclude-from FILE` (one pattern per line, `#` comments).

### 6.3 Directory pruning

If a directory matches `E` (and is not re-included by a later `!` rule in `E`),
its entire subtree is skipped; a file under an excluded directory cannot be
re-included (same rule as git).

## 7. CLI surface (identical in both implementations)

```
packer pack   [-o OUT] [--encrypt] [--split-size SIZE] [--level N]
              [--skip-symlinks] [--include PAT]... [--exclude PAT]...
              [--include-from FILE]... [--exclude-from FILE]...
              [--passphrase-file FILE] [--progress MODE] [-v|--verbose] DIR [DIR...]
packer unpack [-o DESTDIR] [--overwrite] [--passphrase-file FILE] [--progress MODE] [-v|--verbose] INPUT
packer encrypt [-o OUT] [--passphrase-file FILE] [--progress MODE] INPUT
packer decrypt [-o OUT] [--passphrase-file FILE] [--progress MODE] INPUT
packer split   --split-size SIZE [-o OUT] [--progress MODE] INPUT
packer merge   [-o OUT] [--progress MODE] PART_OR_STEM
```

Passphrase resolution order: `--passphrase-file`, then `PACKER_PASSPHRASE`
environment variable, then an interactive no-echo prompt (confirmed twice when
encrypting).

### 7.1 Progress reporting

Every command accepts `--progress MODE` where `MODE` is one of:
- `auto` (default): show live progress only when stderr is a terminal, so
  piped/redirected runs (scripts, the test and bench harnesses, `-o -`) stay
  quiet automatically.
- `on`: always show progress.
- `off`: never show progress.

When active, a single throttled status line is rewritten in place on stderr
(carriage-return, ~10 Hz) reporting the running byte count and throughput, plus
a file count for `pack`/`unpack`. On completion it is replaced by a one-line
summary. Progress is advisory: it goes only to stderr and never affects the
bytes written to the output (so it has no bearing on cross-implementation
compatibility). The byte total reflects the *uncompressed* data processed
(source file contents for `pack`; extracted contents for `unpack`; the stream
for `encrypt`/`decrypt`/`split`/`merge`).

### 7.2 Verbose listing (`-v`/`--verbose`, pack & unpack only)

`--verbose` prints one permanent stderr line per entry, independent of
`--progress` (it is emitted even when stderr is not a terminal). When both are
active, each verbose line first erases the transient progress line, which then
redraws, so the two compose cleanly.

- `pack`: for every command-line target directory, its **top-level** entries
  (immediate children) are listed. A file or symlink prints its path
  (`<arg>/<name>`, with the argument spelled as given, trailing slashes
  trimmed). A directory prints the same path plus its subtree progress as
  `(<N> files, <SIZE>)`, emitted once the whole subtree has been archived.
  Entries removed by the filter (and, with `--skip-symlinks`, symlinks) are not
  listed.
- `unpack`: the **first-level** entries created directly under the destination
  (the archive roots) are listed once each, path only (`<destdir>/<name>`).

Both implementations produce identical verbose text for the same tree. The
per-directory byte totals count uncompressed regular-file contents; directory
and symlink entries contribute to file counts (symlinks count as files,
directories do not).
