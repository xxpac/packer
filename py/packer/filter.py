"""Gitignore-style two-set filtering (see SPEC.md 6).

The translate() function is a direct port of the Go implementation so both
produce identical match behavior; this is validated by a shared fixture table
in the conformance suite.
"""

import re

_META = set(".+()|{}^$")


def _trim_trailing_spaces(s):
    i = len(s)
    while i > 0 and s[i - 1] == " ":
        if i >= 2 and s[i - 2] == "\\":
            break
        i -= 1
    return s[:i]


def _translate(p):
    """Return (regex_body, anchored) for a glob pattern.

    The pattern must already have any leading '!' and trailing '/' removed.
    """
    anchored = "/" in p
    if p.startswith("/"):
        anchored = True
        p = p[1:]
    out = []
    n = len(p)
    i = 0
    while i < n:
        c = p[i]
        if c == "*":
            if i + 1 < n and p[i + 1] == "*":
                j = i
                while j < n and p[j] == "*":
                    j += 1
                prev_slash = i == 0 or p[i - 1] == "/"
                next_slash = j >= n or p[j] == "/"
                if prev_slash and next_slash:
                    if j >= n:
                        out.append(".*")
                    else:
                        out.append("(?:.*/)?")
                        j += 1  # consume following '/'
                else:
                    out.append("[^/]*")
                i = j
                continue
            out.append("[^/]*")
            i += 1
        elif c == "?":
            out.append("[^/]")
            i += 1
        elif c == "[":
            j = i + 1
            if j < n and p[j] in "!^":
                j += 1
            if j < n and p[j] == "]":
                j += 1
            while j < n and p[j] != "]":
                j += 1
            if j >= n:
                out.append("\\[")
                i += 1
            else:
                cls = p[i:j + 1]
                if cls.startswith("[!"):
                    cls = "[^" + cls[2:]
                out.append(cls)
                i = j + 1
        elif c == "\\":
            if i + 1 < n:
                out.append(re.escape(p[i + 1]))
                i += 2
            else:
                out.append("\\\\")
                i += 1
        else:
            if c in _META:
                out.append("\\" + c)
            else:
                out.append(c)
            i += 1
    return "".join(out), anchored


class _Name:
    """One compiled names-mode line, matched against a top-level entry name.
    matched records whether it ever selected anything, so an entry that silently
    covers nothing can be reported."""

    __slots__ = ("src", "re", "matched")

    def __init__(self, src, regex):
        self.src = src
        self.re = regex
        self.matched = False


def _compile_names(lines):
    """Read top-level entry name patterns. Each line is a shell-style glob
    matched against a top-level name; a match covers that entry and, when it is
    a directory, everything beneath it."""
    names = []
    seen = set()
    for raw in lines:
        line = raw.rstrip("\r\n").strip()
        if line == "" or line.startswith("#"):
            continue
        # A leading or trailing slash is a harmless way to spell a directory,
        # so accept it rather than making the user care.
        if line.startswith("/"):
            line = line[1:]
        if line.endswith("/"):
            line = line[:-1]
        if line == "":
            continue
        if "/" in line:
            raise ValueError(
                "names mode: %r is not a top-level entry name (it contains '/')" % line
            )
        if line in seen:
            continue
        seen.add(line)
        # A name is always a whole single component, so anchor it outright
        # rather than using the depth-dependent anchoring of Set.
        body, _ = _translate(line)
        names.append(_Name(line, re.compile("^" + body + "$")))
    return names


class _Pattern:
    __slots__ = ("re", "negate", "dir_only")

    def __init__(self, regex, negate, dir_only):
        self.re = regex
        self.negate = negate
        self.dir_only = dir_only


class Set:
    """An ordered pattern list with last-match-wins semantics, or, when names is
    set, a set of top-level entry name patterns (see SPEC.md 6.4)."""

    def __init__(self, lines, names=False):
        self.patterns = []
        self.names_mode = names
        self.names = _compile_names(lines) if names else []
        if names:
            return
        for raw in lines:
            line = raw.rstrip("\r\n")
            line = _trim_trailing_spaces(line)
            if line == "" or line.startswith("#"):
                continue
            negate = False
            if line.startswith("!"):
                negate = True
                line = line[1:]
            dir_only = False
            if line.endswith("/"):
                dir_only = True
                line = line[:-1]
            if line == "":
                continue
            body, anchored = _translate(line)
            full = ("^" + body + "$") if anchored else ("^(?:.*/)?" + body + "$")
            self.patterns.append(_Pattern(re.compile(full), negate, dir_only))

    def empty(self):
        if self.names_mode:
            return len(self.names) == 0
        return len(self.patterns) == 0

    def unmatched(self):
        """The names-mode lines that never selected an entry, in the order
        given. Meaningful only once a walk has finished."""
        return [n.src for n in self.names if not n.matched]

    def match(self, path, is_dir):
        if self.names_mode:
            # A name covers its whole subtree, so only the first component
            # matters; files and directories are treated alike. Every hit is
            # recorded, not just the first, so unmatched() can tell which lines
            # pulled their weight.
            top = path.split("/", 1)[0]
            hit = False
            for n in self.names:
                if n.re.search(top):
                    n.matched = True
                    hit = True
            return hit
        matched = False
        for p in self.patterns:
            if p.dir_only and not is_dir:
                continue
            if p.re.search(path):
                matched = not p.negate
        return matched


class Filter:
    def __init__(self, include_lines, exclude_lines, names=False):
        self.include = Set(include_lines, names)
        self.exclude = Set(exclude_lines, names)

    def keep(self, path, is_dir):
        included = self.include.empty() or self.include.match(path, is_dir)
        excluded = self.exclude.match(path, is_dir)
        return included and not excluded

    def excludes_dir(self, path):
        return self.exclude.match(path, True)
