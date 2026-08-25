#!/usr/bin/env bash
# Compare the Go and Python implementations on wall-clock time, peak memory and
# output size for pack/unpack across plain / encrypt / split modes.
#
# Usage: bench/run.sh [DATASET_MB]   (default 64)
set -u

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOBIN="$REPO/go/packer"
export PYTHONPATH="$REPO/py"
export PACKER_PASSPHRASE="bench-pass"
export GOPROXY=off GOFLAGS=-mod=mod

DATASET_MB="${1:-64}"
SPLIT_SIZE="10MiB"

TIME=/usr/bin/time
HAVE_TIME=0
[ -x "$TIME" ] && HAVE_TIME=1

echo "building go binary..."
( cd "$REPO/go" && go build -o packer . ) || { echo "go build failed"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

echo "generating ~${DATASET_MB}MB dataset..."
mkdir -p data/text data/rand
half=$(( DATASET_MB * 1024 * 1024 / 2 ))
each_text=$(( half / 8 ))
each_rand=$(( half / 8 ))
for i in $(seq 1 8); do
  yes "The quick brown fox jumps over the lazy dog. line $i padding padding." \
    | head -c "$each_text" > "data/text/t$i.txt"
  head -c "$each_rand" /dev/urandom > "data/rand/r$i.bin"
done

to_seconds() {
  awk -F: '{ if (NF==3) printf "%.2f", $1*3600+$2*60+$3;
             else if (NF==2) printf "%.2f", $1*60+$2;
             else printf "%.2f", $1 }' <<<"$1"
}

M_WALL=0
M_RSS=0
measure() { # command...
  local log; log="$(mktemp)"
  if [ "$HAVE_TIME" = 1 ]; then
    "$TIME" -v "$@" >/dev/null 2>"$log"
    M_RSS="$(grep -m1 'Maximum resident set size' "$log" | grep -oE '[0-9]+')"
    local e; e="$(grep -m1 'Elapsed (wall clock)' "$log" | sed -E 's/.*: //')"
    M_WALL="$(to_seconds "$e")"
  else
    local s en; s="$(date +%s.%N)"; "$@" >/dev/null 2>&1; en="$(date +%s.%N)"
    M_WALL="$(awk "BEGIN{printf \"%.2f\", $en-$s}")"; M_RSS=0
  fi
  rm -f "$log"
}

human() { numfmt --to=iec --suffix=B "$1" 2>/dev/null || echo "$1"; }

out_size() { # base -> total bytes of base or base.*
  if [ -f "$1" ]; then stat -c %s "$1"; else
    local t=0 s
    for p in "$1".*; do s=$(stat -c %s "$p"); t=$((t+s)); done
    echo "$t"
  fi
}

input_arg() { if [ -f "$1" ]; then echo "$1"; else echo "$1.001"; fi; }

printf "\n%-7s %-14s %10s %10s %12s %12s %12s\n" \
  impl mode pack_s unpack_s out_size pack_rss unpack_rss
printf '%s\n' "--------------------------------------------------------------------------------"

run_case() { # impl mode pack_flags...
  local impl="$1" mode="$2"; shift 2
  local pack_flags=("$@")
  local packcmd unpackcmd
  if [ "$impl" = go ]; then
    packcmd=("$GOBIN" pack); unpackcmd=("$GOBIN" unpack)
  else
    packcmd=(python3 -m packer pack); unpackcmd=(python3 -m packer unpack)
  fi

  rm -rf out.pk* restore
  measure "${packcmd[@]}" "${pack_flags[@]}" -o out.pk data
  local pack_s="$M_WALL" pack_rss="$M_RSS"
  local size; size="$(out_size out.pk)"

  measure "${unpackcmd[@]}" -o restore "$(input_arg out.pk)"
  local unpack_s="$M_WALL" unpack_rss="$M_RSS"

  printf "%-7s %-14s %10s %10s %12s %10sKB %10sKB\n" \
    "$impl" "$mode" "$pack_s" "$unpack_s" "$(human "$size")" "$pack_rss" "$unpack_rss"
}

for impl in go py; do
  run_case "$impl" plain
  run_case "$impl" encrypt -e
  run_case "$impl" split --split-size "$SPLIT_SIZE"
done

echo
echo "notes: pack_rss / unpack_rss = peak resident memory (Maximum RSS)."
echo "dataset: ${DATASET_MB}MB (half compressible text, half random); split=${SPLIT_SIZE}."
