package rod

import (
	"context"
	"testing"
)

func TestLongestCommonSubsequenceLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		left       []string
		right      []string
		wantLength int
	}{
		{name: "empty", wantLength: 0},
		{name: "same", left: []string{"a", "b", "c"}, right: []string{"a", "b", "c"}, wantLength: 3},
		{name: "different", left: []string{"a", "b"}, right: []string{"c", "d"}, wantLength: 0},
		{name: "ordered", left: []string{"a", "b", "c", "d"}, right: []string{"b", "d"}, wantLength: 2},
		{name: "duplicates", left: []string{"a", "b", "a", "b"}, right: []string{"a", "a", "b"}, wantLength: 3},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := longestCommonSubsequenceLength(context.Background(), test.left, test.right); got != test.wantLength {
				t.Fatalf("length = %d, want %d", got, test.wantLength)
			}
		})
	}
}

func TestLongestCommonSubsequenceCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := longestCommonSubsequenceLength(ctx, []string{"a"}, []string{"a"}); got != 0 {
		t.Fatalf("length = %d", got)
	}
}

func TestLongestCommonSubsequenceMatchesDynamicProgramming(t *testing.T) {
	t.Parallel()

	sequences := [][]string{{}}
	for length := 1; length <= 5; length++ {
		for bits := 0; bits < 1<<length; bits++ {
			sequence := make([]string, length)
			for i := range length {
				sequence[i] = string(rune('a' + (bits>>i)&1))
			}
			sequences = append(sequences, sequence)
		}
	}

	for _, left := range sequences {
		for _, right := range sequences {
			got := longestCommonSubsequenceLength(context.Background(), left, right)
			want := dynamicLCSLength(left, right)
			if got != want {
				t.Fatalf("length(%v, %v) = %d, want %d", left, right, got, want)
			}
		}
	}
}

func dynamicLCSLength(left, right []string) int {
	row := make([]int, len(right)+1)
	for _, x := range left {
		previous := 0
		for j, y := range right {
			current := row[j+1]
			if x == y {
				row[j+1] = previous + 1
			} else {
				row[j+1] = max(row[j], row[j+1])
			}
			previous = current
		}
	}
	return row[len(right)]
}
