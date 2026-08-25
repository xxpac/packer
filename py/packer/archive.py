"""Tar archive layer: symlink-safe create + hardened extract (see SPEC.md 2)."""

import os
import sys
import tarfile

from .progress import Progress


def _join_display(disp, name):
    """Path shown in verbose mode: the input arg as typed (trailing slashes
    trimmed) plus the child name. Kept identical to the Go implementation."""
    return disp.rstrip("/") + "/" + name


def create(fileobj, dirs, skip_symlinks=False, flt=None, progress=None):
    """Write a tar stream of dirs to fileobj. Symlinks are stored, never followed."""
    if progress is None:
        progress = Progress()
    tf = tarfile.open(fileobj=fileobj, mode="w|", format=tarfile.GNU_FORMAT)
    tf.dereference = False
    try:
        seen = {}
        for d in dirs:
            base = os.path.abspath(d)
            root = os.path.basename(base.rstrip("/"))
            if root in ("", ".", "/"):
                raise ValueError("cannot derive an archive name for %r" % d)
            if root in seen:
                raise ValueError(
                    "duplicate archive root %r from inputs %r and %r"
                    % (root, seen[root], d)
                )
            seen[root] = d
            _add_tree(tf, base, root, skip_symlinks, flt, progress, d)
    finally:
        tf.close()


def _add_entry(tf, path, arcname, progress):
    ti = tf.gettarinfo(name=path, arcname=arcname)
    if ti is None:
        return
    if ti.isreg():
        with open(path, "rb") as f:
            tf.addfile(ti, progress.wrap_reader(f))
        progress.add_file()
    else:
        tf.addfile(ti)
        if ti.issym():
            progress.add_file()


def _add_tree(tf, base, root, skip_symlinks, flt, progress, disp):
    _add_entry(tf, base, root, progress)

    def recurse(dirpath, relprefix):
        # Direct children of a command-line target dir are the "top level"
        # entries reported in verbose mode.
        top_level = relprefix == ""
        with os.scandir(dirpath) as it:
            entries = sorted(it, key=lambda e: e.name)
        for e in entries:
            rel = (relprefix + "/" + e.name) if relprefix else e.name
            arc = root + "/" + rel
            if e.is_symlink():
                if skip_symlinks:
                    continue
                if flt is not None and not flt.keep(rel, False):
                    continue
                _add_entry(tf, e.path, arc, progress)
                if top_level:
                    progress.log_file(_join_display(disp, e.name))
                continue
            if e.is_dir(follow_symlinks=False):
                if flt is not None and flt.excludes_dir(rel):
                    continue
                f0, b0 = progress.snapshot()
                if flt is None or flt.keep(rel, True):
                    _add_entry(tf, e.path, arc, progress)
                recurse(e.path, rel)
                if top_level:
                    f1, b1 = progress.snapshot()
                    progress.log_dir(_join_display(disp, e.name), f1 - f0, b1 - b0)
                continue
            if not e.is_file(follow_symlinks=False):
                sys.stderr.write("packer: skipping special file %s\n" % rel)
                continue
            if flt is not None and not flt.keep(rel, False):
                continue
            _add_entry(tf, e.path, arc, progress)
            if top_level:
                progress.log_file(_join_display(disp, e.name))

    recurse(base, "")


def _safe_target(dest_abs, name):
    target = os.path.normpath(os.path.join(dest_abs, name))
    if target != dest_abs and not target.startswith(dest_abs + os.sep):
        raise ValueError("unsafe path in archive: %r" % name)
    return target


def _ensure_no_symlink_parent(root, parent):
    rel = os.path.relpath(parent, root)
    if rel == ".":
        return
    cur = root
    for part in rel.split(os.sep):
        cur = os.path.join(cur, part)
        try:
            st = os.lstat(cur)
        except FileNotFoundError:
            return
        if os.path.islink(cur) or (st.st_mode & 0o170000) == 0o120000:
            raise ValueError("refusing to extract through symlink %r" % cur)


def extract(fileobj, dest, overwrite=False, progress=None):
    if progress is None:
        progress = Progress()
    dest_abs = os.path.abspath(dest)
    os.makedirs(dest_abs, exist_ok=True)
    tf = tarfile.open(fileobj=fileobj, mode="r|")
    dir_modes = []
    logged_top = set()
    for ti in tf:
        target = _safe_target(dest_abs, ti.name)
        parent = os.path.dirname(target)
        _ensure_no_symlink_parent(dest_abs, parent)
        os.makedirs(parent, exist_ok=True)

        # Verbose: report each first-level entry (direct child of the
        # destination, i.e. an archive root) once, path only.
        if progress.verbose:
            name = ti.name.rstrip("/")
            if name and "/" not in name and name not in logged_top:
                logged_top.add(name)
                progress.log_file(_join_display(dest, name))

        if ti.isdir():
            os.makedirs(target, exist_ok=True)
            dir_modes.append((target, ti.mode & 0o7777))
        elif ti.issym():
            _remove_if_exists(target, overwrite)
            os.symlink(ti.linkname, target)
            progress.add_file()
        elif ti.isreg():
            _remove_if_exists(target, overwrite)
            with open(target, "wb") as f:
                src = tf.extractfile(ti)
                if src is not None:
                    _copy(src, f, progress=progress)
            os.chmod(target, ti.mode & 0o7777)
            os.utime(target, (ti.mtime, ti.mtime))
            progress.add_file()
        else:
            sys.stderr.write("packer: skipping unsupported entry %r\n" % ti.name)

    for target, mode in sorted(dir_modes, key=lambda t: len(t[0]), reverse=True):
        os.chmod(target, mode)


def _copy(src, dst, bufsize=1 << 16, progress=None):
    while True:
        chunk = src.read(bufsize)
        if not chunk:
            break
        dst.write(chunk)
        if progress is not None:
            progress.add_bytes(len(chunk))


def _remove_if_exists(target, overwrite):
    if not os.path.lexists(target):
        return
    if os.path.isdir(target) and not os.path.islink(target):
        raise ValueError("cannot overwrite directory with file/symlink: %r" % target)
    if not overwrite:
        raise ValueError("destination exists (use --overwrite): %r" % target)
    os.remove(target)
