"""Split/merge layer (see SPEC.md 5)."""

import os
import re

_SUFFIX_RE = re.compile(r"^(.*)\.(\d+)$")

_UNITS = [
    ("KIB", 1 << 10), ("MIB", 1 << 20), ("GIB", 1 << 30), ("TIB", 1 << 40),
    ("KB", 10 ** 3), ("MB", 10 ** 6), ("GB", 10 ** 9), ("TB", 10 ** 12),
    ("K", 1 << 10), ("M", 1 << 20), ("G", 1 << 30), ("T", 1 << 40),
    ("B", 1),
]


def parse_size(s):
    s = s.strip()
    if not s:
        raise ValueError("empty size")
    up = s.upper()
    for suffix, mult in _UNITS:
        if up.endswith(suffix):
            num = up[: len(up) - len(suffix)].strip()
            v = int(num)
            if v < 0:
                raise ValueError("negative size")
            return v * mult
    return int(up)


class SplitWriter:
    """A binary writer that splits into '<base>.NNN' parts of at most size bytes."""

    def __init__(self, base, size):
        self.base = base
        self.size = size
        self.part = 0
        self.cur = None
        self.written = 0
        self.parts = []

    def write(self, data):
        mv = memoryview(bytes(data))
        total = 0
        while len(mv):
            if self.cur is None or self.written >= self.size:
                self._rotate()
            space = self.size - self.written
            take = min(len(mv), space)
            self.cur.write(mv[:take])
            self.written += take
            total += take
            mv = mv[take:]
        return total

    def _rotate(self):
        if self.cur is not None:
            self.cur.close()
        self.part += 1
        name = "%s.%03d" % (self.base, self.part)
        self.cur = open(name, "wb")
        self.written = 0
        self.parts.append(name)

    def flush(self):
        if self.cur is not None:
            self.cur.flush()

    def close(self):
        if self.cur is None:
            self._rotate()
        if self.cur is not None:
            self.cur.close()
            self.cur = None


def stem(arg):
    m = _SUFFIX_RE.match(arg)
    return m.group(1) if m else arg


def find_parts(arg):
    st = stem(arg)
    directory = os.path.dirname(st) or "."
    base = os.path.basename(st)
    part_re = re.compile(r"^" + re.escape(base) + r"\.(\d+)$")
    found = []
    try:
        names = os.listdir(directory)
    except FileNotFoundError:
        names = []
    for name in names:
        m = part_re.match(name)
        if m and os.path.isfile(os.path.join(directory, name)):
            found.append((int(m.group(1)), os.path.join(directory, name)))
    if not found:
        raise FileNotFoundError("no parts found for %r" % arg)
    found.sort(key=lambda t: t[0])
    return [p for _, p in found]


class PartsReader:
    """A binary reader that streams the concatenation of parts in order."""

    def __init__(self, parts):
        self.parts = list(parts)
        self.idx = 0
        self.cur = None

    def _ensure(self):
        while self.cur is None:
            if self.idx >= len(self.parts):
                return False
            self.cur = open(self.parts[self.idx], "rb")
            self.idx += 1
        return True

    def read(self, n=-1):
        if n is None or n < 0:
            out = bytearray()
            while self._ensure():
                out += self.cur.read()
                self.cur.close()
                self.cur = None
            return bytes(out)
        out = bytearray()
        while len(out) < n:
            if not self._ensure():
                break
            chunk = self.cur.read(n - len(out))
            if not chunk:
                self.cur.close()
                self.cur = None
                continue
            out += chunk
        return bytes(out)

    def close(self):
        if self.cur is not None:
            self.cur.close()
            self.cur = None
