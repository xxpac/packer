#!/usr/bin/env bash
# Conformance suite: within-implementation and cross-implementation round trips,
# plus the shared filter parity table. Exits non-zero if anything fails.
set -u

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GOBIN="$REPO/go/packer"
export PYTHONPATH="$REPO/py"
export PACKER_PASSPHRASE="conformance-pass"
export GOPROXY=off GOFLAGS=-mod=mod

PASS=0
FAIL=0
ok()  { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }

GO() { "$GOBIN" "$@"; }
PY() { python3 -m packer "$@"; }

echo "== building go binary =="
( cd "$REPO/go" && go build -o packer . ) || { echo "go build failed"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

# ---- build a rich fixture tree ----
mkdir -p src/sub/deeper src/build src/empty_dir
printf 'alpha'        > src/a.txt
printf ''             > src/empty.txt
printf 'package foo'  > src/sub/c.go
printf 'logline'      > src/sub/app.log
printf 'obj'          > src/build/ignored.o
head -c 200000 /dev/urandom > src/sub/deeper/big.bin   # > 3 encryption chunks
ln -s ../a.txt src/sub/link
chmod 640 src/a.txt
chmod 600 src/sub/c.go
chmod 750 src/sub

snapshot() {
  local root="$1"
  ( cd "$root"
    find . -printf '%y %m %p -> %l\n' | LC_ALL=C sort
    find . -type f -print0 | LC_ALL=C sort -z | while IFS= read -r -d '' fp; do
      printf 'H %s %s\n' "$fp" "$(sha256sum "$fp" | cut -d' ' -f1)"
    done
  )
}

check_tree() { # expected_root actual_root label
  if diff <(snapshot "$1") <(snapshot "$2") >/dev/null; then
    ok "$3"
  else
    echo "  ---- diff for: $3 ----"
    diff <(snapshot "$1") <(snapshot "$2") | head -40
    bad "$3"
  fi
}

input_arg() { # base -> echo the file or first part
  if [ -f "$1" ]; then echo "$1"; else echo "$1.001"; fi
}

# ---- within-implementation round trips ----
for impl in go py; do
  case "$impl" in
    go) RUN=GO ;;
    py) RUN=PY ;;
  esac
  echo "== within-impl round trips: $impl =="
  rm -rf w.pk* out
  "$RUN" pack -o w.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg w.pk)" >/dev/null
  check_tree src out/src "$impl plain"

  rm -rf w.pk* out
  "$RUN" pack -e -o w.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg w.pk)" >/dev/null
  check_tree src out/src "$impl encrypted (multi-chunk)"

  rm -rf w.pk* out
  "$RUN" pack --split-size 64KiB -o w.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg w.pk)" >/dev/null
  check_tree src out/src "$impl split"

  rm -rf w.pk* out
  "$RUN" pack -e --split-size 64KiB -o w.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg w.pk)" >/dev/null
  check_tree src out/src "$impl encrypted+split"

  rm -rf w.pk* out
  "$RUN" pack --skip-symlinks -o w.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg w.pk)" >/dev/null
  if diff <(snapshot src | grep -v '^l ') <(snapshot out/src) >/dev/null; then
    ok "$impl skip-symlinks"
  else
    echo "  ---- diff for: $impl skip-symlinks ----"
    diff <(snapshot src | grep -v '^l ') <(snapshot out/src) | head -40
    bad "$impl skip-symlinks"
  fi

  rm -rf w.pk* out
  "$RUN" pack --exclude '*.log' --exclude 'build/' -o w.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg w.pk)" >/dev/null
  if [ ! -e out/src/sub/app.log ] && [ ! -e out/src/build ] && [ -e out/src/sub/c.go ]; then
    ok "$impl filtered (exclude)"
  else
    bad "$impl filtered (exclude)"
  fi
done

# ---- cross-implementation round trips ----
echo "== cross-impl round trips =="
rm -rf x.pk* out
GO pack -o x.pk src >/dev/null && PY unpack -o out "$(input_arg x.pk)" >/dev/null
check_tree src out/src "go pack -> py unpack (plain)"

rm -rf x.pk* out
PY pack -o x.pk src >/dev/null && GO unpack -o out "$(input_arg x.pk)" >/dev/null
check_tree src out/src "py pack -> go unpack (plain)"

rm -rf x.pk* out
GO pack -e --split-size 64KiB -o x.pk src >/dev/null && PY unpack -o out "$(input_arg x.pk)" >/dev/null
check_tree src out/src "go pack -> py unpack (encrypt+split)"

rm -rf x.pk* out
PY pack -e --split-size 64KiB -o x.pk src >/dev/null && GO unpack -o out "$(input_arg x.pk)" >/dev/null
check_tree src out/src "py pack -> go unpack (encrypt+split)"

# standalone encrypt/decrypt cross
GO encrypt -o ge.enc src/sub/deeper/big.bin >/dev/null && PY decrypt -o ge.out ge.enc >/dev/null
cmp -s src/sub/deeper/big.bin ge.out && ok "go encrypt -> py decrypt" || bad "go encrypt -> py decrypt"
PY encrypt -o pe.enc src/sub/deeper/big.bin >/dev/null && GO decrypt -o pe.out pe.enc >/dev/null
cmp -s src/sub/deeper/big.bin pe.out && ok "py encrypt -> go decrypt" || bad "py encrypt -> go decrypt"

# standalone split/merge cross
GO split --split-size 7777 -o gsp src/sub/deeper/big.bin >/dev/null && PY merge -o gsp.out gsp.001 >/dev/null
cmp -s src/sub/deeper/big.bin gsp.out && ok "go split -> py merge" || bad "go split -> py merge"
PY split --split-size 5555 -o psp src/sub/deeper/big.bin >/dev/null && GO merge -o psp.out psp.001 >/dev/null
cmp -s src/sub/deeper/big.bin psp.out && ok "py split -> go merge" || bad "py split -> go merge"

# ---- verbose listing vs. the filter (SPEC 7.2) ----
echo "== verbose listing =="

vraw() { # RUN args... -> the per-entry verbose lines for `pack ... src`
  local run="$1"; shift
  rm -f v.pk
  # The final summary is drawn with a leading CR and carries a timing-dependent
  # rate, so strip it (and the output-path line) before comparing.
  "$run" pack --progress off -v -o v.pk "$@" src 2>&1 | tr -d '\r' |
    grep -v -e '^packer: packed ' -e '^packer: wrote '
}

check_verbose() { # label expected_paths args...
  local label="$1" want="$2"; shift 2
  local g p got
  g="$(vraw GO "$@")"
  p="$(vraw PY "$@")"
  got="$(printf '%s\n' "$g" | sed -n 's|^packer: \(src/[^ ]*\).*|\1|p')"
  if [ "$got" = "$want" ]; then
    ok "verbose listing: $label"
  else
    echo "  ---- expected vs listed: $label ----"
    diff <(printf '%s\n' "$want") <(printf '%s\n' "$got")
    bad "verbose listing: $label"
  fi
  if [ "$g" = "$p" ]; then
    ok "verbose parity: $label"
  else
    echo "  ---- go vs py verbose text: $label ----"
    diff <(printf '%s\n' "$g") <(printf '%s\n' "$p")
    bad "verbose parity: $label"
  fi
}

check_verbose "no filter" \
"src/a.txt
src/build
src/empty.txt
src/empty_dir
src/sub"

# A dir whose whole subtree the include set drops must not be listed, while one
# that contributes only through its contents (its own entry dropped) must be.
check_verbose "include subtree" \
"src/sub" --include 'sub/**'

# A kept directory is listed even when it archives no files of its own.
check_verbose "include empty dir" \
"src/empty_dir" --include 'empty_dir/'

check_verbose "exclude dir" \
"src/a.txt
src/empty.txt
src/empty_dir
src/sub" --exclude 'build/'

# ---- names mode (SPEC 6.4) ----
echo "== names mode =="
for impl in go py; do
  case "$impl" in
    go) RUN=GO ;;
    py) RUN=PY ;;
  esac

  # An included directory name pulls in its whole subtree, dir entries (and so
  # their modes) included.
  rm -rf n.pk* out
  "$RUN" pack --names --include sub -o n.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg n.pk)" >/dev/null
  check_tree src/sub out/src/sub "$impl names include (whole subtree)"
  if [ ! -e out/src/a.txt ] && [ ! -e out/src/build ]; then
    ok "$impl names include (unlisted entries dropped)"
  else
    bad "$impl names include (unlisted entries dropped)"
  fi

  # An excluded directory name removes the whole subtree.
  rm -rf n.pk* out
  "$RUN" pack --names --exclude build --exclude sub -o n.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg n.pk)" >/dev/null
  if [ ! -e out/src/build ] && [ ! -e out/src/sub ] &&
     [ -e out/src/a.txt ] && [ -e out/src/empty_dir ]; then
    ok "$impl names exclude (whole subtree)"
  else
    bad "$impl names exclude (whole subtree)"
  fi

  # A path rather than a bare name is rejected instead of silently matching
  # nothing, which is the mistake this mode exists to prevent.
  rm -rf n.pk*
  if "$RUN" pack --names --include sub/deeper -o n.pk src >/dev/null 2>&1; then
    bad "$impl names rejects a path"
  else
    ok "$impl names rejects a path"
  fi

  # Names take shell globs, matched against top-level entries only.
  rm -rf n.pk* out
  "$RUN" pack --names --include '*.txt' -o n.pk src >/dev/null
  "$RUN" unpack -o out "$(input_arg n.pk)" >/dev/null
  if [ -e out/src/a.txt ] && [ -e out/src/empty.txt ] && [ ! -e out/src/sub ]; then
    ok "$impl names glob"
  else
    bad "$impl names glob"
  fi

  # A name that selects nothing is reported rather than silently ignored.
  rm -rf n.pk*
  got="$("$RUN" pack --names --include sub --include nope --exclude alsonope \
          -o n.pk src 2>&1 >/dev/null | tr -d '\r' | grep 'matched nothing')"
  want='packer: warning: include name "nope" matched nothing
packer: warning: exclude name "alsonope" matched nothing'
  if [ "$got" = "$want" ]; then
    ok "$impl names warns on unmatched"
  else
    echo "  ---- unmatched-name warnings: $impl ----"
    diff <(printf '%s\n' "$want") <(printf '%s\n' "$got")
    bad "$impl names warns on unmatched"
  fi

  # A name that does match must stay quiet.
  rm -rf n.pk*
  if [ -z "$("$RUN" pack --names --include sub -o n.pk src 2>&1 >/dev/null |
             grep 'matched nothing')" ]; then
    ok "$impl names quiet when matched"
  else
    bad "$impl names quiet when matched"
  fi
done

# ---- filter parity ----
echo "== filter parity table =="
if ( cd "$REPO/go" && go test ./internal/filter/ -run TestFilterParity >/dev/null 2>&1 ); then
  ok "go filter parity"
else
  bad "go filter parity"
fi
if python3 "$REPO/tests/conformance/test_filter.py" >/dev/null 2>&1; then
  ok "py filter parity"
else
  bad "py filter parity"
fi

echo
echo "==================================="
echo "conformance: $PASS passed, $FAIL failed"
echo "==================================="
[ "$FAIL" -eq 0 ]
