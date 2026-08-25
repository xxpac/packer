"""Gzip compression layer (see SPEC.md 3)."""

import gzip

DEFAULT_LEVEL = 6


def gzip_writer(fileobj, level=DEFAULT_LEVEL):
    # mtime=0 for reproducible output.
    return gzip.GzipFile(fileobj=fileobj, mode="wb", compresslevel=level, mtime=0)


def gzip_reader(fileobj):
    return gzip.GzipFile(fileobj=fileobj, mode="rb")
