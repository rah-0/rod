package guardian

import (
	"reflect"
	"testing"
)

func TestEachPID(t *testing.T) {
	for input, want := range map[string][]int{
		"":                       nil,
		"12 345 6 ":              {12, 345, 6},
		" 7\n8\t9":               {7, 8, 9},
		"0 -5 x1 2y 10":          {10},
		"99999999999999999999 3": {3},
	} {
		var got []int
		eachPID([]byte(input), func(pid int) { got = append(got, pid) })
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", input, got, want)
		}
	}
}
