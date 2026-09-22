package cdp

import (
	"bufio"
	"strings"
	"testing"
)

type repeatingFrameReader struct {
	frame  []byte
	offset int
}

func (r *repeatingFrameReader) Read(p []byte) (int, error) {
	n := copy(p, r.frame[r.offset:])
	r.offset += n
	if r.offset == len(r.frame) {
		r.offset = 0
	}
	return n, nil
}

type websocketReadBenchmark struct {
	name       string
	size       int
	fragmented bool
}

func BenchmarkWebSocketRead(b *testing.B) {
	benchmarks := []websocketReadBenchmark{
		{name: "64B", size: 64},
		{name: "1KiB", size: 1024},
		{name: "32KiB", size: 32 * 1024},
		{name: "1MiB", size: 1024 * 1024},
		{name: "32KiBFragmented", size: 32 * 1024, fragmented: true},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			payload := []byte(strings.Repeat("x", benchmark.size))
			frame := serverFrame(1, true, payload)
			if benchmark.fragmented {
				frame = serverFrame(1, false, payload[:len(payload)/2])
				frame = append(frame, serverFrame(0, true, payload[len(payload)/2:])...)
			}
			ws := &WebSocket{r: bufio.NewReader(&repeatingFrameReader{frame: frame})}
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				message, err := ws.Read()
				if err != nil {
					b.Fatal(err)
				}
				if len(message) != len(payload) {
					b.Fatalf("message size = %d, want %d", len(message), len(payload))
				}
			}
		})
	}
}
