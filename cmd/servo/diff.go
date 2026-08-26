package main

import (
	"fmt"
	"strings"
)

type diffKind int

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffKind
	line string
}

// unifiedDiff renders a compact, +/- only diff (no unchanged-line
// context/hunk headers) between oldContent and newContent, labeled path,
// for `servo check` to print alongside its stale-file error.
func unifiedDiff(oldContent, newContent, path string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s (committed)\n+++ %s (fresh)\n", path, path)
	for _, op := range diffLines(oldLines, newLines) {
		switch op.kind {
		case diffDelete:
			fmt.Fprintf(&b, "-%s\n", op.line)
		case diffInsert:
			fmt.Fprintf(&b, "+%s\n", op.line)
		}
	}
	return b.String()
}

// diffLines is a standard LCS-backtrack diff: fine for generated-file
// sizes (hundreds of lines), no external dependency needed.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{diffEqual, a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{diffDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{diffInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{diffDelete, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{diffInsert, b[j]})
	}
	return ops
}
