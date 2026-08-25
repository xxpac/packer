"""Magic-byte sniffing for unpack auto-detection (see SPEC.md 1)."""

from .crypto import MAGIC as PACKENC_MAGIC


def is_gzip(b):
    return len(b) >= 2 and b[0] == 0x1F and b[1] == 0x8B


def is_packenc(b):
    return b[: len(PACKENC_MAGIC)] == PACKENC_MAGIC


class Peekable:
    """Wraps a binary reader with a peek() that does not consume."""

    def __init__(self, r):
        self.r = r
        self.buf = b""

    def peek(self, n):
        while len(self.buf) < n:
            chunk = self.r.read(n - len(self.buf))
            if not chunk:
                break
            self.buf += chunk
        return self.buf[:n]

    def read(self, n=-1):
        if n is None or n < 0:
            data = self.buf + (self.r.read() or b"")
            self.buf = b""
            return data
        if not self.buf:
            return self.r.read(n) or b""
        take = self.buf[:n]
        self.buf = self.buf[n:]
        if len(take) < n:
            more = self.r.read(n - len(take)) or b""
            return take + more
        return take

    def close(self):
        closer = getattr(self.r, "close", None)
        if closer:
            closer()
