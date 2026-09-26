package cdp

import (
	"bufio"
	"strconv"
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

type discardConn struct{ frameConn }

func (*discardConn) Write(p []byte) (int, error) { return len(p), nil }

// BenchmarkWebSocketSend measures client frame construction, including masking.
func BenchmarkWebSocketSend(b *testing.B) {
	for _, size := range []int{64, 1024, 32 * 1024, 1024 * 1024} {
		name := strconv.Itoa(size) + "B"
		if size >= 1024*1024 {
			name = strconv.Itoa(size/(1024*1024)) + "MiB"
		} else if size >= 1024 {
			name = strconv.Itoa(size/1024) + "KiB"
		}
		b.Run(name, func(b *testing.B) {
			payload := []byte(strings.Repeat("x", size))
			ws := &WebSocket{conn: &discardConn{}}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := ws.Send(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
