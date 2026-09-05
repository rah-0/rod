package rod

import (
	"context"
	"sort"
)

// longestCommonSubsequenceLength uses the Hunt-Szymanski reduction from LCS
// to LIS. The position lists are descending so duplicates are handled without
// reusing an item from the left sequence.
func longestCommonSubsequenceLength(ctx context.Context, left, right []string) int {
	positions := make(map[string][]int, len(left))
	for i := len(left) - 1; i >= 0; i-- {
		positions[left[i]] = append(positions[left[i]], i)
	}

	tails := make([]int, 0, min(len(left), len(right)))
	for _, item := range right {
		if ctx.Err() != nil {
			return len(tails)
		}
		for _, position := range positions[item] {
			i := sort.SearchInts(tails, position)
			if i == len(tails) {
				tails = append(tails, position)
			} else {
				tails[i] = position
			}
		}
	}
	return len(tails)
}
