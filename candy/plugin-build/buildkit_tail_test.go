package build

import (
	"strings"
	"testing"
)

// TestTailLines is the coverage for opencharly/charly#1255: a failed build must carry the
// captured command output in its error instead of only `exit status 1`. tailLines is what keeps
// that bounded; if it truncates from the wrong end, the failing STEP (the last lines) is lost and
// the fix proves nothing.
func TestTailLines(t *testing.T) {
	cases := []struct {
		name, out string
		n         int
		want      string
	}{
		{"empty", "", 10, ""},
		{"short", "#1 a\n#2 b\n", 10, "#1 a\n#2 b"},
		{"exact", "a\nb", 2, "a\nb"},
		{"tail", "a\nb\nc\nd", 2, "c\nd"},
		{"tail-keeps-the-error-line", "#13 RUN x\n#13 ERROR: pacman failed\n", 1, "#13 ERROR: pacman failed"},
	}
	for _, c := range cases {
		if got := tailLines(c.out, c.n); got != c.want {
			t.Fatalf("%s: tailLines(%q,%d) = %q, want %q", c.name, c.out, c.n, got, c.want)
		}
	}
	if got := tailLines("only", 1); strings.HasSuffix(got, "\n") {
		t.Fatalf("trailing newline leaked into the error: %q", got)
	}
}
