package filter

import (
	"os"
	"strings"
	"testing"
)

// TestFilterParity runs the shared fixture table that both the Go and Python
// implementations must satisfy (tests/fixtures/filter_cases.tsv).
func TestFilterParity(t *testing.T) {
	const fixture = "../../../tests/fixtures/filter_cases.tsv"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	pats := func(field string) []string {
		if field == "" {
			return nil
		}
		return strings.Split(field, "|")
	}
	n := 0
	for lineNo, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 5 && len(cols) != 6 {
			t.Fatalf("line %d: expected 5 or 6 columns, got %d: %q", lineNo+1, len(cols), line)
		}
		compile := Compile
		if len(cols) == 6 && cols[5] == "names" {
			compile = CompileNames
		}
		inc, err := compile(pats(cols[0]))
		if err != nil {
			t.Fatalf("line %d: compile include: %v", lineNo+1, err)
		}
		exc, err := compile(pats(cols[1]))
		if err != nil {
			t.Fatalf("line %d: compile exclude: %v", lineNo+1, err)
		}
		f := &Filter{Include: inc, Exclude: exc}
		path := cols[2]
		isDir := cols[3] == "1"
		want := cols[4] == "1"
		got := f.Keep(path, isDir)
		if got != want {
			t.Errorf("line %d: Keep(inc=%q, exc=%q, path=%q, dir=%v) = %v, want %v",
				lineNo+1, cols[0], cols[1], path, isDir, got, want)
		}
		n++
	}
	t.Logf("checked %d filter cases", n)
}
