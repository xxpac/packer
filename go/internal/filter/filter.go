package filter

import (
	"fmt"
	"regexp"
	"strings"
)

// A pattern is a single compiled gitignore-style line.
type pattern struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// A namePattern is one compiled names-mode line, matched against a top-level
// entry name. matched records whether it ever selected anything, so an entry
// that silently covers nothing can be reported.
type namePattern struct {
	src     string
	re      *regexp.Regexp
	matched bool
}

// Set is an ordered list of patterns evaluated with last-match-wins semantics,
// or, when built by CompileNames, a set of top-level entry name patterns.
type Set struct {
	patterns  []pattern
	names     []namePattern
	namesMode bool
	empty     bool
}

// Empty reports whether the set has no effective patterns.
func (s *Set) Empty() bool { return s == nil || s.empty }

// Compile builds a Set from gitignore-style lines. See SPEC.md section 6.1.
func Compile(lines []string) (*Set, error) {
	s := &Set{}
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		// Trim trailing unescaped spaces.
		line = trimTrailingSpaces(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		neg := false
		if strings.HasPrefix(line, "!") {
			neg = true
			line = line[1:]
		}
		dirOnly := false
		if strings.HasSuffix(line, "/") {
			dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		body, anchored := translate(line)
		var full string
		if anchored {
			full = "^" + body + "$"
		} else {
			full = "^(?:.*/)?" + body + "$"
		}
		re, err := regexp.Compile(full)
		if err != nil {
			return nil, err
		}
		s.patterns = append(s.patterns, pattern{re: re, negate: neg, dirOnly: dirOnly})
	}
	s.empty = len(s.patterns) == 0
	return s, nil
}

// CompileNames builds a Set from top-level entry name patterns. Each line is a
// shell-style glob matched against a top-level name; a match covers that entry
// and, when it is a directory, everything beneath it. See SPEC.md section 6.4.
func CompileNames(lines []string) (*Set, error) {
	s := &Set{namesMode: true}
	seen := map[string]bool{}
	for _, raw := range lines {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r\n"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// A leading or trailing slash is a harmless way to spell a directory,
		// so accept it rather than making the user care.
		line = strings.TrimSuffix(strings.TrimPrefix(line, "/"), "/")
		if line == "" {
			continue
		}
		if strings.Contains(line, "/") {
			return nil, fmt.Errorf("names mode: %q is not a top-level entry name (it contains %q)", line, "/")
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		// A name is always a whole single component, so anchor it outright
		// rather than using the depth-dependent anchoring of Compile.
		body, _ := translate(line)
		re, err := regexp.Compile("^" + body + "$")
		if err != nil {
			return nil, err
		}
		s.names = append(s.names, namePattern{src: line, re: re})
	}
	s.empty = len(s.names) == 0
	return s, nil
}

// Unmatched returns the names-mode lines that never selected an entry, in the
// order given. It is meaningful only once a walk has finished.
func (s *Set) Unmatched() []string {
	if s == nil || !s.namesMode {
		return nil
	}
	var out []string
	for i := range s.names {
		if !s.names[i].matched {
			out = append(out, s.names[i].src)
		}
	}
	return out
}

func trimTrailingSpaces(s string) string {
	i := len(s)
	for i > 0 && s[i-1] == ' ' {
		// A space escaped by a backslash is kept.
		if i >= 2 && s[i-2] == '\\' {
			break
		}
		i--
	}
	return s[:i]
}

// Match reports whether path (forward slashes, relative to an input root) is
// matched by the set, honoring negation with last-match-wins.
func (s *Set) Match(path string, isDir bool) bool {
	if s == nil {
		return false
	}
	if s.namesMode {
		// A name covers its whole subtree, so only the first component matters;
		// files and directories are treated alike. Every hit is recorded, not
		// just the first, so Unmatched can tell which lines pulled their weight.
		top := path
		if i := strings.IndexByte(path, '/'); i >= 0 {
			top = path[:i]
		}
		hit := false
		for i := range s.names {
			if s.names[i].re.MatchString(top) {
				s.names[i].matched = true
				hit = true
			}
		}
		return hit
	}
	matched := false
	for _, p := range s.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if p.re.MatchString(path) {
			matched = !p.negate
		}
	}
	return matched
}

// translate converts a glob pattern (without the leading '!' or trailing '/')
// into a regular-expression body and reports whether it is anchored to root.
func translate(p string) (string, bool) {
	anchored := strings.Contains(p, "/")
	if strings.HasPrefix(p, "/") {
		anchored = true
		p = p[1:]
	}
	var b strings.Builder
	n := len(p)
	i := 0
	for i < n {
		c := p[i]
		switch c {
		case '*':
			if i+1 < n && p[i+1] == '*' {
				j := i
				for j < n && p[j] == '*' {
					j++
				}
				prevSlash := i == 0 || p[i-1] == '/'
				nextSlash := j >= n || p[j] == '/'
				if prevSlash && nextSlash {
					if j >= n {
						b.WriteString(".*")
					} else {
						b.WriteString("(?:.*/)?")
						j++ // consume the following '/'
					}
				} else {
					b.WriteString("[^/]*")
				}
				i = j
				continue
			}
			b.WriteString("[^/]*")
			i++
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			j := i + 1
			if j < n && (p[j] == '!' || p[j] == '^') {
				j++
			}
			if j < n && p[j] == ']' {
				j++
			}
			for j < n && p[j] != ']' {
				j++
			}
			if j >= n {
				b.WriteString("\\[")
				i++
			} else {
				cls := p[i : j+1]
				if strings.HasPrefix(cls, "[!") {
					cls = "[^" + cls[2:]
				}
				b.WriteString(cls)
				i = j + 1
			}
		case '\\':
			if i+1 < n {
				b.WriteString(regexp.QuoteMeta(string(p[i+1])))
				i += 2
			} else {
				b.WriteString("\\\\")
				i++
			}
		default:
			if strings.IndexByte(`.+()|{}^$`, c) >= 0 {
				b.WriteByte('\\')
				b.WriteByte(c)
			} else {
				b.WriteByte(c)
			}
			i++
		}
	}
	return b.String(), anchored
}

// Filter combines an include (allowlist) set and an exclude (blocklist) set with
// the two-set semantics from SPEC.md section 6.2.
type Filter struct {
	Include *Set
	Exclude *Set
}

// Keep reports whether a path should be packed.
func (f *Filter) Keep(path string, isDir bool) bool {
	included := f.Include.Empty() || f.Include.Match(path, isDir)
	excluded := f.Exclude != nil && f.Exclude.Match(path, isDir)
	return included && !excluded
}

// ExcludesDir reports whether a directory is excluded (used for subtree pruning).
func (f *Filter) ExcludesDir(path string) bool {
	return f.Exclude != nil && f.Exclude.Match(path, true)
}
