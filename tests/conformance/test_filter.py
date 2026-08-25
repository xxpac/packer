#!/usr/bin/env python3
"""Run the shared filter parity table against the Python implementation.

This reads the same tests/fixtures/filter_cases.tsv that the Go test uses, so
both implementations are guaranteed to agree on include/exclude behavior.
"""

import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "..", "py"))

from packer.filter import Filter  # noqa: E402

FIXTURE = os.path.join(HERE, "..", "fixtures", "filter_cases.tsv")


def pats(field):
    return field.split("|") if field else []


def main():
    failures = 0
    checked = 0
    with open(FIXTURE) as f:
        for line_no, line in enumerate(f, 1):
            line = line.rstrip("\n")
            if not line.strip() or line.startswith("#"):
                continue
            cols = line.split("\t")
            if len(cols) != 5:
                print("line %d: expected 5 columns, got %d: %r" % (line_no, len(cols), line))
                failures += 1
                continue
            flt = Filter(pats(cols[0]), pats(cols[1]))
            path = cols[2]
            is_dir = cols[3] == "1"
            want = cols[4] == "1"
            got = flt.keep(path, is_dir)
            if got != want:
                print("line %d: keep(inc=%r, exc=%r, path=%r, dir=%s) = %s, want %s"
                      % (line_no, cols[0], cols[1], path, is_dir, got, want))
                failures += 1
            checked += 1
    if failures:
        print("FILTER PARITY FAILED: %d/%d cases" % (failures, checked))
        return 1
    print("filter parity OK: %d cases" % checked)
    return 0


if __name__ == "__main__":
    sys.exit(main())
