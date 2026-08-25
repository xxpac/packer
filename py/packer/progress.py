"""Lightweight, throttled progress reporting to stderr.

Mirrors go/internal/progress so both implementations report the same way. A
disabled meter is a cheap no-op and wrap_reader() returns the stream unchanged,
so there is no overhead when progress is off (e.g. piped/redirected runs).
"""

import sys
import time

_UNITS = ["KiB", "MiB", "GiB", "TiB", "PiB"]


def human(n):
    """Format a byte count with IEC binary units (matches the Go implementation)."""
    if n < 1024:
        return "%d B" % int(n)
    f = float(n)
    i = -1
    while f >= 1024 and i < len(_UNITS) - 1:
        f /= 1024.0
        i += 1
    return "%.1f %s" % (f, _UNITS[i])


def enabled(mode, stream=None):
    """Resolve a --progress mode ('auto'|'on'|'off') to a bool.

    'auto' turns progress on only when the stream is a terminal, so piped or
    redirected runs stay quiet by default.
    """
    stream = stream if stream is not None else sys.stderr
    if mode == "on":
        return True
    if mode == "off":
        return False
    # "auto" (and anything argparse let through)
    try:
        return stream.isatty()
    except Exception:
        return False


class Progress:
    """A throttled progress meter.

    ing/ed are the present ("packing") and past ("packed") tense verbs shown in
    the live line and the final summary. When disabled every method is a no-op.
    """

    def __init__(self, ing="", ed="", is_enabled=False, count_files=False,
                 verbose=False, interval=0.1, stream=None):
        self.ing = ing
        self.ed = ed
        self.enabled = is_enabled
        self.verbose = verbose
        self.count_files = count_files
        self.interval = interval
        self.stream = stream if stream is not None else sys.stderr
        self.files = 0
        self.bytes = 0
        self.start = time.monotonic()
        self.last = 0.0
        self.last_width = 0

    def _active(self):
        # Counting runs whenever we draw a live line OR emit verbose lines.
        return self.enabled or self.verbose

    def add_bytes(self, n):
        if not self._active():
            return
        self.bytes += n
        if self.enabled:
            self._draw(False)

    def add_file(self):
        if not self._active():
            return
        self.files += 1
        if self.enabled:
            self._draw(False)

    def snapshot(self):
        """Return the current (files, bytes) counters, for per-subtree deltas."""
        return self.files, self.bytes

    def wrap_reader(self, fileobj):
        """Return a reader that counts bytes as they are read, or fileobj as-is
        when inactive (so tarfile/addfile pays no wrapper cost when off)."""
        if not self._active():
            return fileobj
        return _CountingReader(fileobj, self)

    def log_file(self, path):
        """Print a permanent line for a top-level file (verbose only)."""
        if not self.verbose:
            return
        self._log("packer: " + path)

    def log_dir(self, path, files, nbytes):
        """Print a top-level directory plus its subtree progress (verbose only)."""
        if not self.verbose:
            return
        self._log("packer: %s (%d files, %s)" % (path, files, human(nbytes)))

    def _log(self, s):
        # Erase the transient live line (if any) so verbose output and the
        # in-place meter can coexist.
        if self.last_width > 0:
            self.stream.write("\r" + (" " * self.last_width) + "\r")
            self.last_width = 0
        self.stream.write(s + "\n")
        self.stream.flush()

    def _stats(self, now):
        elapsed = now - self.start
        rate = self.bytes / elapsed if elapsed > 0 else 0
        if self.count_files:
            return "%d files  %s  %s/s" % (self.files, human(self.bytes), human(rate))
        return "%s  %s/s" % (human(self.bytes), human(rate))

    def _draw(self, force):
        now = time.monotonic()
        if not force and (now - self.last) < self.interval:
            return
        self.last = now
        line = "packer: %s  %s" % (self.ing, self._stats(now))
        pad = max(self.last_width - len(line), 0)
        self.stream.write("\r" + line + (" " * pad))
        self.stream.flush()
        self.last_width = len(line)

    def finish(self):
        """Overwrite the transient line with a permanent one-line summary."""
        if not self._active():
            return
        now = time.monotonic()
        elapsed = now - self.start
        rate = self.bytes / elapsed if elapsed > 0 else 0
        if self.count_files:
            summary = "packer: %s %d files, %s in %.1fs (%s/s)" % (
                self.ed, self.files, human(self.bytes), elapsed, human(rate))
        else:
            summary = "packer: %s %s in %.1fs (%s/s)" % (
                self.ed, human(self.bytes), elapsed, human(rate))
        pad = max(self.last_width - len(summary), 0)
        self.stream.write("\r" + summary + (" " * pad) + "\n")
        self.stream.flush()
        self.last_width = 0


class _CountingReader:
    """Wraps a binary reader, counting bytes read into a Progress meter."""

    def __init__(self, fileobj, progress):
        self._f = fileobj
        self._p = progress

    def read(self, n=-1):
        data = self._f.read(n)
        if data:
            self._p.add_bytes(len(data))
        return data
