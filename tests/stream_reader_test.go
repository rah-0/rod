package rod_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/proto"
)

func TestStreamReaderFinalData(t *testing.T) {
	for _, response := range []string{
		`{"data":"last bytes","eof":true}`,
		`{"data":"bGFzdCBieXRlcw==","base64Encoded":true,"eof":true}`,
	} {
		calls := 0
		client := &callTestClient{call: func(context.Context, string, any) ([]byte, error) {
			calls++
			return []byte(response), nil
		}}
		reader := rod.NewStreamReader(client, "stream")
		if n, err := reader.Read(nil); n != 0 || err != nil || calls != 0 {
			t.Fatalf("empty read: n=%d err=%v calls=%d", n, err, calls)
		}
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != "last bytes" || calls != 1 {
			t.Fatalf("final data: %q err=%v calls=%d", data, err, calls)
		}
		if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) || calls != 1 {
			t.Fatalf("read after EOF: n=%d err=%v calls=%d", n, err, calls)
		}
	}
}

func TestStreamReaderDrainsBufferedDataBeforeError(t *testing.T) {
	calls := 0
	disconnected := errors.New("stream disconnected")
	client := &callTestClient{call: func(context.Context, string, any) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"data":"abcdef","eof":false}`), nil
		}
		return nil, disconnected
	}}
	reader := rod.NewStreamReader(client, "stream")
	buffer := make([]byte, 2)
	for _, want := range []string{"ab", "cd", "ef"} {
		if n, err := reader.Read(buffer); n != 2 || err != nil || string(buffer) != want || calls != 1 {
			t.Fatalf("buffered read: %q n=%d err=%v calls=%d, want %q", buffer, n, err, calls, want)
		}
	}
	if _, err := reader.Read(buffer); !errors.Is(err, disconnected) || calls != 2 {
		t.Fatalf("read after buffer: err=%v calls=%d", err, calls)
	}
}

func TestStreamReaderAdvancesExplicitOffset(t *testing.T) {
	calls := 0
	client := &callTestClient{call: func(_ context.Context, method string, params any) ([]byte, error) {
		request := params.(proto.IORead)
		if method != "IO.read" || request.Offset == nil || request.Size == nil || *request.Size != 2 {
			t.Fatalf("read request: %s %+v", method, request)
		}
		calls++
		if calls == 1 {
			if *request.Offset != 7 {
				t.Fatalf("initial offset = %d", *request.Offset)
			}
			return []byte(`{"data":"AP8B","base64Encoded":true,"eof":false}`), nil
		}
		if *request.Offset != 10 {
			t.Fatalf("next offset = %d, want decoded-byte offset 10", *request.Offset)
		}
		return []byte(`{"data":"","eof":true}`), nil
	}}
	reader := rod.NewStreamReader(client, "stream")
	reader.Offset = new(7)
	buffer := make([]byte, 2)
	if n, err := reader.Read(buffer); n != 2 || err != nil || *reader.Offset != 9 {
		t.Fatalf("first read: n=%d err=%v offset=%d", n, err, *reader.Offset)
	}
	if n, err := reader.Read(buffer); n != 1 || err != nil || calls != 1 || *reader.Offset != 10 {
		t.Fatalf("buffered read: n=%d err=%v calls=%d offset=%d", n, err, calls, *reader.Offset)
	}
	if _, err := reader.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("final read: %v", err)
	}
}

// streamServer answers IO.read like Chrome: at most Size bytes from Offset, or
// from the end of the previous read when Offset is omitted.
type streamServer struct {
	data  []byte
	next  int
	reads []proto.IORead
}

func (s *streamServer) Call(_ context.Context, _, method string, params any) ([]byte, error) {
	request, ok := params.(proto.IORead)
	if !ok || method != request.ProtoReq() {
		return nil, fmt.Errorf("unexpected call %s", method)
	}
	s.reads = append(s.reads, request)
	if request.Offset != nil {
		s.next = *request.Offset
	}
	start := min(s.next, len(s.data))
	end := len(s.data)
	if request.Size != nil {
		end = min(end, start+*request.Size)
	}
	s.next = end
	return json.Marshal(proto.IOReadResult{
		Base64Encoded: true,
		Data:          base64.StdEncoding.EncodeToString(s.data[start:end]),
		EOF:           end == len(s.data),
	})
}

func TestStreamReaderChunkSize(t *testing.T) {
	const chunk = 1 << 20
	data := append(bytes.Repeat([]byte("0123456789abcdef"), chunk/16), "tail"...)
	read := func(chunkSize int, readAll func(io.Reader) ([]byte, error)) []int {
		t.Helper()
		server := &streamServer{data: data}
		reader := rod.NewStreamReader(server, "stream")
		reader.ChunkSize = chunkSize
		got, err := readAll(reader)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("ChunkSize %d: read %d bytes, error %v", chunkSize, len(got), err)
		}
		var sizes []int
		for _, request := range server.reads {
			if request.Size == nil || request.Offset != nil {
				t.Fatalf("read request: %+v", request)
			}
			sizes = append(sizes, *request.Size)
		}
		return sizes
	}
	copyAll := func(r io.Reader) ([]byte, error) {
		var out bytes.Buffer
		// Hiding ReadFrom makes io.Copy use 32 KiB buffers, like utils.OutputFile.
		_, err := io.Copy(struct{ io.Writer }{&out}, r)
		return out.Bytes(), err
	}

	if sizes := read(chunk, copyAll); !slices.Equal(sizes, []int{chunk, chunk}) {
		t.Errorf("ChunkSize %d with 32 KiB buffers requested %v", chunk, sizes)
	}
	// Zero requests the buffer length, as streams of a loading body need.
	if sizes := read(0, copyAll); len(sizes) != 33 || sizes[0] != 32<<10 {
		t.Errorf("ChunkSize 0 with 32 KiB buffers requested %v", sizes)
	}
	// A buffer larger than ChunkSize is requested whole.
	readFull := func(r io.Reader) ([]byte, error) {
		buffer := make([]byte, len(data))
		n, err := io.ReadFull(r, buffer)
		return buffer[:n], err
	}
	if sizes := read(chunk, readFull); !slices.Equal(sizes, []int{len(data)}) {
		t.Errorf("ChunkSize %d with a %d byte buffer requested %v", chunk, len(data), sizes)
	}
}

func TestStreamReaderOffsetTracksReturnedBytes(t *testing.T) {
	server := &streamServer{data: []byte("0123456789")}
	reader := rod.NewStreamReader(server, "stream")
	reader.ChunkSize = 16
	reader.Offset = new(2)
	buffer := make([]byte, 3)
	for _, want := range []string{"234", "567"} {
		if n, err := reader.Read(buffer); err != nil || string(buffer[:n]) != want {
			t.Fatalf("read %q, error %v; want %q", buffer[:n], err, want)
		}
	}
	if len(server.reads) != 1 || *server.reads[0].Offset != 2 || *reader.Offset != 8 {
		t.Fatalf("reads=%d offset=%d", len(server.reads), *reader.Offset)
	}

	// Another reader resumes at Offset without skipping buffered data.
	resumed := rod.NewStreamReader(server, "stream")
	resumed.Offset = new(*reader.Offset)
	if data, err := io.ReadAll(resumed); err != nil || string(data) != "89" {
		t.Fatalf("resumed read %q, error %v", data, err)
	}

	// Setting Offset after sequential reads seeks even when the value matches
	// the number of bytes this reader returned.
	server.next = 5
	sequential := rod.NewStreamReader(server, "stream")
	sequential.ChunkSize = 16
	if n, err := sequential.Read(make([]byte, 2)); n != 2 || err != nil {
		t.Fatalf("sequential read: n=%d err=%v", n, err)
	}
	sequential.Offset = new(2)
	if data, err := io.ReadAll(sequential); err != nil || string(data) != "23456789" {
		t.Fatalf("read after assigning Offset: %q, error %v", data, err)
	}

	// Assigning Offset discards buffered data, including after EOF.
	for _, position := range []int{1, 9} {
		*reader.Offset = position
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != "0123456789"[position:] || *reader.Offset != 10 {
			t.Fatalf("read from %d: %q, error %v, offset %d", position, data, err, *reader.Offset)
		}
		if request := server.reads[len(server.reads)-1]; *request.Offset != position {
			t.Fatalf("request offset %d, want %d", *request.Offset, position)
		}
	}
}
