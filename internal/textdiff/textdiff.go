// Package textdiff shows how one text became another, as a unified diff.
package textdiff

import (
	"fmt"
	"strings"
)

// maxCells bounds the comparison table; texts that differ over more lines
// than this are shown as removed and added in whole.
const maxCells = 4 << 20

type op struct {
	kind byte // ' ', '-', '+'
	text string
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// Unified returns the changes from a to b with three lines of context,
// without file headers; "" when they are the same.
func Unified(a, b string) string {
	x, y := split(a), split(b)
	// The common start and end need no comparing.
	pre := 0
	for pre < len(x) && pre < len(y) && x[pre] == y[pre] {
		pre++
	}
	suf := 0
	for suf < len(x)-pre && suf < len(y)-pre && x[len(x)-1-suf] == y[len(y)-1-suf] {
		suf++
	}
	if pre == len(x) && pre == len(y) {
		return ""
	}
	var ops []op
	for _, l := range x[:pre] {
		ops = append(ops, op{' ', l})
	}
	ops = append(ops, middle(x[pre:len(x)-suf], y[pre:len(y)-suf])...)
	for _, l := range x[len(x)-suf:] {
		ops = append(ops, op{' ', l})
	}
	return hunks(ops, 3)
}

// middle compares the differing part by longest common subsequence.
func middle(x, y []string) []op {
	n, m := len(x), len(y)
	var ops []op
	if n*m > maxCells {
		for _, l := range x {
			ops = append(ops, op{'-', l})
		}
		for _, l := range y {
			ops = append(ops, op{'+', l})
		}
		return ops
	}
	// lcs[i*(m+1)+j] is the common length of x[i:] and y[j:].
	lcs := make([]int32, (n+1)*(m+1))
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i*(m+1)+j] = lcs[(i+1)*(m+1)+j+1] + 1
			} else {
				lcs[i*(m+1)+j] = max(lcs[(i+1)*(m+1)+j], lcs[i*(m+1)+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			ops = append(ops, op{' ', x[i]})
			i++
			j++
		case lcs[(i+1)*(m+1)+j] >= lcs[i*(m+1)+j+1]:
			ops = append(ops, op{'-', x[i]})
			i++
		default:
			ops = append(ops, op{'+', y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', x[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', y[j]})
	}
	return ops
}

// hunks groups changes with ctx lines around them.
func hunks(ops []op, ctx int) string {
	var b strings.Builder
	for start := 0; start < len(ops); {
		// Find the next change.
		first := start
		for first < len(ops) && ops[first].kind == ' ' {
			first++
		}
		if first == len(ops) {
			break
		}
		lo := max(first-ctx, 0)
		// Extend while changes are closer than two contexts apart.
		hi, gap := first, 0
		for k := first; k < len(ops); k++ {
			if ops[k].kind == ' ' {
				gap++
				if gap > 2*ctx {
					break
				}
				continue
			}
			gap, hi = 0, k
		}
		end := min(hi+ctx+1, len(ops))
		// Line numbers where the hunk starts in each text.
		aLine, bLine := 1, 1
		for _, o := range ops[:lo] {
			if o.kind != '+' {
				aLine++
			}
			if o.kind != '-' {
				bLine++
			}
		}
		aN, bN := 0, 0
		for _, o := range ops[lo:end] {
			if o.kind != '+' {
				aN++
			}
			if o.kind != '-' {
				bN++
			}
		}
		if aN == 0 {
			aLine--
		}
		if bN == 0 {
			bLine--
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", aLine, aN, bLine, bN)
		for _, o := range ops[lo:end] {
			b.WriteByte(o.kind)
			b.WriteString(o.text)
			b.WriteByte('\n')
		}
		start = end
	}
	return b.String()
}

// Count returns how many lines were added and removed.
func Count(diff string) (added, removed int) {
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return added, removed
}
