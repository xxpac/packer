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
