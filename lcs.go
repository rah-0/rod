package rod

import (
	"context"
	"slices"
)

// longestCommonSubsequenceLength uses the Hunt-Szymanski reduction from LCS
// to LIS. The position lists are descending so duplicates are handled without
// reusing an item from the left sequence.
func longestCommonSubsequenceLength(ctx context.Context, left, right []string) int {
	// Stable snapshots usually share all or most strings. Remove matching ends
	// before building the position index for the changed portion.
	common := 0
	for len(left) > 0 && len(right) > 0 && left[0] == right[0] {
		if ctx.Err() != nil {
			return common
		}
		common++
		left, right = left[1:], right[1:]
	}
	for len(left) > 0 && len(right) > 0 && left[len(left)-1] == right[len(right)-1] {
		if ctx.Err() != nil {
			return common
		}
		common++
		left, right = left[:len(left)-1], right[:len(right)-1]
	}
	if len(left) == 0 || len(right) == 0 {
		return common
	}

	positions := make(map[string][]int, len(left))
	for i, item := range slices.Backward(left) {
		if ctx.Err() != nil {
			return common
		}
		positions[item] = append(positions[item], i)
	}

	tails := make([]int, 0, min(len(left), len(right)))
	for _, item := range right {
		if ctx.Err() != nil {
			return common + len(tails)
		}
		for _, position := range positions[item] {
			i, _ := slices.BinarySearch(tails, position)
			if i == len(tails) {
				tails = append(tails, position)
			} else {
				tails[i] = position
			}
		}
	}
	return common + len(tails)
}
