package secureexec

import (
	"os"
	"runtime"
	"strconv"
)

func platformSecure() bool {
	auxv, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return true
	}
	little := true
	switch runtime.GOARCH {
	case "mips", "mips64", "ppc64", "s390x":
		little = false
	}
	return secureAuxv(auxv, strconv.IntSize/8, little)
}

func secureAuxv(data []byte, word int, little bool) bool {
	decode := func(data []byte) uint64 {
		var value uint64
		for i := range word {
			index := i
			if little {
				index = word - 1 - i
			}
			value = value<<8 | uint64(data[index])
		}
		return value
	}
	for len(data) >= 2*word {
		key, value := decode(data[:word]), decode(data[word:2*word])
		if key == 23 { // AT_SECURE includes file-capability transitions.
			return value != 0
		}
		if key == 0 {
			break
		}
		data = data[2*word:]
	}
	return true
}
