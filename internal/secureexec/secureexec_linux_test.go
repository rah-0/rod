package secureexec

import "testing"

func TestSecureAuxiliaryVector(t *testing.T) {
	for _, word := range []int{4, 8} {
		for _, little := range []bool{false, true} {
			for _, secure := range []byte{0, 1} {
				data := make([]byte, 4*word)
				index := word - 1
				if little {
					index = 0
				}
				data[index], data[word+index] = 23, secure
				if got := secureAuxv(data, word, little); got != (secure != 0) {
					t.Fatalf("AT_SECURE=%d, word=%d little=%t: %t", secure, word, little, got)
				}
			}
		}
	}
	for _, data := range [][]byte{nil, {1}, make([]byte, 16)} {
		if !secureAuxv(data, 8, true) {
			t.Fatal("missing secure marker did not fail closed")
		}
	}
}
