package textdiff

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// apply puts a diff made by Unified onto a.
func apply(t *testing.T, a, diff string) string {
	t.Helper()
	x := split(a)
	var out []string
	at := 0 // next line of x not yet copied
	lines := split(diff)
	for k := 0; k < len(lines); {
		var aStart, aN, bStart, bN int
		if _, err := fmt.Sscanf(lines[k], "@@ -%d,%d +%d,%d @@", &aStart, &aN, &bStart, &bN); err != nil {
			t.Fatalf("bad hunk header %q", lines[k])
		}
		from := aStart - 1
		if aN == 0 {
			from = aStart
		}
		out = append(out, x[at:from]...)
		at = from
		k++
		for ; k < len(lines) && !strings.HasPrefix(lines[k], "@@"); k++ {
			l := lines[k]
			switch l[0] {
			case ' ':
				if x[at] != l[1:] {
					t.Fatalf("context %q does not match %q", l[1:], x[at])
				}
				out = append(out, x[at])
				at++
			case '-':
				if x[at] != l[1:] {
					t.Fatalf("removed %q does not match %q", l[1:], x[at])
				}
				at++
			case '+':
				out = append(out, l[1:])
			}
		}
	}
	out = append(out, x[at:]...)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

func TestUnifiedRoundTrips(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	gen := func() string {
		n := r.Intn(40)
		var l []string
		for i := 0; i < n; i++ {
			l = append(l, "line "+strconv.Itoa(r.Intn(12)))
		}
		if n == 0 {
			return ""
		}
		return strings.Join(l, "\n") + "\n"
	}
	for i := 0; i < 3000; i++ {
		a, b := gen(), gen()
		d := Unified(a, b)
		if a == b {
			if d != "" {
				t.Fatalf("same text gave a diff:\n%s", d)
			}
			continue
		}
		if got := apply(t, a, d); got != b {
			t.Fatalf("round trip failed\na=%q\nb=%q\ndiff:\n%s\ngot=%q", a, b, d, got)
		}
	}
}

func TestUnifiedShape(t *testing.T) {
	a := "server {\n    listen 80;\n    server_name a.com;\n    root /www;\n}\n"
	b := "server {\n    listen 80;\n    server_name a.com b.com;\n    root /www;\n}\n"
	want := "@@ -1,5 +1,5 @@\n server {\n     listen 80;\n-    server_name a.com;\n+    server_name a.com b.com;\n     root /www;\n }\n"
	if got := Unified(a, b); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if add, del := Count(want); add != 1 || del != 1 {
		t.Fatalf("count %d %d", add, del)
	}
	if got := Unified("", "x\n"); got != "@@ -0,0 +1,1 @@\n+x\n" {
		t.Fatalf("new file: %q", got)
	}
}
