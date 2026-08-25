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


class _Pattern:
    __slots__ = ("re", "negate", "dir_only")

    def __init__(self, regex, negate, dir_only):
        self.re = regex
        self.negate = negate
        self.dir_only = dir_only


class Set:
    def __init__(self, lines):
        self.patterns = []
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
        return len(self.patterns) == 0

    def match(self, path, is_dir):
        matched = False
        for p in self.patterns:
            if p.dir_only and not is_dir:
                continue
            if p.re.search(path):
                matched = not p.negate
        return matched


class Filter:
    def __init__(self, include_lines, exclude_lines):
        self.include = Set(include_lines)
        self.exclude = Set(exclude_lines)

    def keep(self, path, is_dir):
        included = self.include.empty() or self.include.match(path, is_dir)
        excluded = self.exclude.match(path, is_dir)
        return included and not excluded

    def excludes_dir(self, path):
        return self.exclude.match(path, True)
