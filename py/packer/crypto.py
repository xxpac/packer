"""PACKENC encryption layer: scrypt KDF + chunked AES-256-GCM (see SPEC.md 4)."""

import base64
import hashlib
import json
import os
import struct

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

MAGIC = b"PACKENC\x01"
DEFAULT_N = 1 << 15
DEFAULT_R = 8
DEFAULT_P = 1
DEFAULT_KEYLEN = 32
DEFAULT_CHUNK = 64 * 1024
SALT_LEN = 16
NONCE_PREFIX_LEN = 7
TAG_LEN = 16


def derive_key(passphrase, salt, n, r, p, keylen):
    maxmem = 2 * 128 * r * n + (1 << 20)
    return hashlib.scrypt(
        passphrase, salt=salt, n=n, r=r, p=p, dklen=keylen, maxmem=maxmem
    )


def _read_exact(src, n):
    """Read exactly n bytes (or fewer only at EOF)."""
    buf = bytearray()
    while len(buf) < n:
        chunk = src.read(n - len(buf))
        if not chunk:
            break
        buf.extend(chunk)
    return bytes(buf)


def _nonce(prefix, counter, last):
    return prefix + struct.pack(">I", counter) + (b"\x01" if last else b"\x00")


class EncryptWriter:
    """A binary writer that emits a PACKENC stream to an underlying sink.

    Full chunks are only emitted once strictly more data is buffered, so the
    final chunk (flushed on close) is always correctly flagged -- matching the
    pull-based Go encryptor byte-for-byte in framing.
    """

    def __init__(self, sink, passphrase, chunk_size=DEFAULT_CHUNK,
                 n=DEFAULT_N, r=DEFAULT_R, p=DEFAULT_P, keylen=DEFAULT_KEYLEN):
        self.sink = sink
        self.chunk = chunk_size
        self.counter = 0
        self.buf = bytearray()
        self._closed = False

        salt = os.urandom(SALT_LEN)
        self.prefix = os.urandom(NONCE_PREFIX_LEN)
        key = derive_key(passphrase, salt, n, r, p, keylen)
        self.aes = AESGCM(key)

        header = {
            "version": 1,
            "kdf": "scrypt",
            "salt": base64.b64encode(salt).decode(),
            "scrypt": {"N": n, "r": r, "p": p},
            "keylen": keylen,
            "cipher": "aes-256-gcm",
            "nonce_prefix": base64.b64encode(self.prefix).decode(),
            "chunk_size": chunk_size,
        }
        hb = json.dumps(header, separators=(",", ":")).encode()
        sink.write(MAGIC)
        sink.write(struct.pack(">I", len(hb)))
        sink.write(hb)

    def write(self, data):
        self.buf += bytes(data)
        while len(self.buf) > self.chunk:
            self._emit(bytes(self.buf[: self.chunk]), False)
            del self.buf[: self.chunk]
        return len(data)

    def _emit(self, pt, last):
        nonce = _nonce(self.prefix, self.counter, last)
        self.sink.write(self.aes.encrypt(nonce, pt, None))
        self.counter += 1

    def flush(self):
        pass

    def close(self):
        if self._closed:
            return
        self._emit(bytes(self.buf), True)
        self.buf = bytearray()
        self._closed = True
        self.sink.close()


class DecryptReader:
    """A binary reader that yields plaintext from a PACKENC stream."""

    def __init__(self, src, passphrase):
        magic = _read_exact(src, len(MAGIC))
        if magic != MAGIC:
            raise ValueError("not a PACKENC stream: bad magic")
        (hlen,) = struct.unpack(">I", _read_exact(src, 4))
        if hlen == 0 or hlen > (1 << 20):
            raise ValueError("invalid PACKENC header length")
        header = json.loads(_read_exact(src, hlen))
        if header.get("kdf") != "scrypt" or header.get("cipher") != "aes-256-gcm":
            raise ValueError("unsupported kdf/cipher")
        salt = base64.b64decode(header["salt"])
        self.prefix = base64.b64decode(header["nonce_prefix"])
        if len(self.prefix) != NONCE_PREFIX_LEN:
            raise ValueError("invalid nonce prefix")
        sp = header["scrypt"]
        key = derive_key(passphrase, salt, sp["N"], sp["r"], sp["p"], header["keylen"])
        self.aes = AESGCM(key)
        self.chunk = int(header["chunk_size"])
        if self.chunk <= 0:
            raise ValueError("invalid chunk size")
        self.enc = self.chunk + TAG_LEN
        self.src = src
        self.counter = 0
        self.done = False
        self.out = bytearray()
        self._pending = _read_exact(src, self.enc)

    def _next(self):
        if self.done:
            return b""
        cur = self._pending
        nxt = _read_exact(self.src, self.enc)
        last = len(nxt) == 0
        if len(cur) < TAG_LEN:
            raise ValueError("corrupt or truncated ciphertext")
        nonce = _nonce(self.prefix, self.counter, last)
        try:
            pt = self.aes.decrypt(nonce, cur, None)
        except Exception as e:  # cryptography.exceptions.InvalidTag
            raise ValueError(
                "decryption failed (wrong passphrase or corrupt/truncated data)"
            ) from e
        self.counter += 1
        self._pending = nxt
        if last:
            self.done = True
        return pt

    def read(self, n=-1):
        if n is None or n < 0:
            parts = [bytes(self.out)]
            self.out = bytearray()
            while not self.done:
                parts.append(self._next())
            return b"".join(parts)
        while len(self.out) < n and not self.done:
            self.out += self._next()
        take = bytes(self.out[:n])
        del self.out[:n]
        return take


def encrypt_stream(dst, src, passphrase, chunk_size=DEFAULT_CHUNK):
    """Convenience wrapper for the standalone 'encrypt' command."""
    ew = EncryptWriter(_NoCloseSink(dst), passphrase, chunk_size=chunk_size)
    while True:
        data = src.read(chunk_size)
        if not data:
            break
        ew.write(data)
    ew.close()


def decrypt_stream(dst, src, passphrase, chunk_size=DEFAULT_CHUNK):
    """Convenience wrapper for the standalone 'decrypt' command."""
    dr = DecryptReader(src, passphrase)
    while True:
        data = dr.read(chunk_size)
        if not data:
            break
        dst.write(data)


class _NoCloseSink:
    """Wrap a writer so EncryptWriter.close() does not close the caller's file."""

    def __init__(self, w):
        self.w = w

    def write(self, b):
        return self.w.write(b)

    def close(self):
        pass
