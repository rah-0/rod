package goroutines

import (
	"strings"
	"testing"
)

func TestParseTrace(t *testing.T) {
	raw := `goroutine 42 [chan receive, 2 minutes]:
example.com/project.(*worker).run(0xc000000001)
	/tmp/project/worker file.go:27 +0x2a
created by example.com/project.start in goroutine 1
	/tmp/project/main.go:11 +0x10
[originating from goroutine 7]:
example.com/project.parent(...)
	/tmp/project/parent.go:9 +0x1
[originating from goroutine 1]:
example.com/project.main(...)
	/tmp/project/main.go:5 +0x1`

	trace := parseTrace(raw)
	if trace.Raw != raw {
		t.Fatalf("raw trace changed:\n%s", trace.Raw)
	}
	if trace.GoroutineID != 42 {
		t.Fatalf("goroutine ID = %d, want 42", trace.GoroutineID)
	}
	if trace.WaitReason != "chan receive, 2 minutes" {
		t.Fatalf("wait reason = %q", trace.WaitReason)
	}
	if len(trace.Stacks) != 2 {
		t.Fatalf("stacks = %#v", trace.Stacks)
	}
	if trace.Stacks[0] != (Stack{
		Func: "example.com/project.(*worker).run",
		Loc:  "/tmp/project/worker file.go:27",
	}) {
		t.Fatalf("first stack = %#v", trace.Stacks[0])
	}
	if trace.Stacks[1] != (Stack{
		Func: "example.com/project.start",
		Loc:  "/tmp/project/main.go:11",
	}) {
		t.Fatalf("created-by stack = %#v", trace.Stacks[1])
	}
	if len(trace.GoroutineAncestorIDs) != 2 ||
		trace.GoroutineAncestorIDs[0] != 7 ||
		trace.GoroutineAncestorIDs[1] != 1 {
		t.Fatalf("ancestor IDs = %v", trace.GoroutineAncestorIDs)
	}
	if !trace.HasParent(7) || trace.HasParent(99) {
		t.Fatalf("unexpected parent lookup for %v", trace.GoroutineAncestorIDs)
	}
}

func TestParseTraceMalformedInput(t *testing.T) {
	inputs := []string{
		"",
		"not a goroutine trace",
		"goroutine nope [running]:\nfunction()",
		"goroutine 8 [running]:\nfunction()\nnot-a-location",
		"goroutine 9 [running]:\n[originating from goroutine nope]:",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			traces := parseTraces(input)
			if input == "" {
				if len(traces) != 0 {
					t.Fatalf("empty input produced %d traces", len(traces))
				}
				return
			}
			if len(traces) != 1 {
				t.Fatalf("malformed input produced %d traces", len(traces))
			}
		})
	}

	if Functions("anything")(&Trace{}) {
		t.Fatal("a trace without stacks matched a function")
	}
}

func TestParseHeader(t *testing.T) {
	tests := []struct {
		line   string
		id     int64
		reason string
		ok     bool
	}{
		{"goroutine 1 [running]:", 1, "running", true},
		{"goroutine 25 gp=0xc000104540 m=nil [select, 3 minutes]:", 25, "select, 3 minutes", true},
		{"goroutine nope [running]:", 0, "", false},
		{"goroutine 1 []:", 0, "", false},
		{"not a header", 0, "", false},
	}

	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			id, reason, ok := parseHeader(test.line)
			if id != test.id || reason != test.reason || ok != test.ok {
				t.Fatalf("parseHeader() = (%d, %q, %t)", id, reason, ok)
			}
		})
	}
}

func TestSnapshotAndCurrent(t *testing.T) {
	current := Snapshot(false)
	if len(current) != 1 {
		t.Fatalf("current snapshot contains %d traces", len(current))
	}
	if current[0].GoroutineID <= 0 || len(current[0].Stacks) == 0 {
		t.Fatalf("current trace was not parsed: %#v", current[0])
	}

	ignore := Current()
	current = Snapshot(false)
	if !ignore(current[0]) {
		t.Fatal("Current did not recognize the calling goroutine")
	}
}

func TestTraceFilters(t *testing.T) {
	trace := &Trace{Stacks: []Stack{{Func: "example.block"}}}
	if !Functions("example.block")(trace) {
		t.Fatal("Functions did not match the first frame")
	}
	if Functions("example.other")(trace) {
		t.Fatal("Functions matched a different frame")
	}
	if !Combine(nil, Functions("example.block"))(trace) {
		t.Fatal("Combine did not match an ignore rule")
	}
	if Combine()(trace) {
		t.Fatal("empty Combine matched a trace")
	}

	filtered := Traces{trace, &Trace{Stacks: []Stack{{Func: "example.keep"}}}}.
		Filter(Functions("example.block"))
	if len(filtered) != 1 || filtered[0].Stacks[0].Func != "example.keep" {
		t.Fatalf("filtered traces = %#v", filtered)
	}
}

func TestNonChildren(t *testing.T) {
	t.Setenv("GODEBUG", "other=1,tracebackancestors=10")
	current := Snapshot(false)
	if len(current) == 0 {
		t.Fatal("current snapshot is empty")
	}

	ignore := NonChildren()
	child := &Trace{GoroutineAncestorIDs: []int64{current[0].GoroutineID}}
	if ignore(child) {
		t.Fatal("child goroutine was ignored")
	}
	if !ignore(&Trace{GoroutineAncestorIDs: []int64{999}}) {
		t.Fatal("unrelated goroutine was not ignored")
	}
}

func TestNonChildrenRequiresAncestry(t *testing.T) {
	t.Setenv("GODEBUG", "tracebackancestors=0")
	defer func() {
		if recover() == nil {
			t.Fatal("NonChildren did not reject disabled ancestry")
		}
	}()
	NonChildren()
}

func TestTracebackAncestorsEnabled(t *testing.T) {
	tests := map[string]bool{
		"":                               false,
		"tracebackancestors=0":           false,
		"tracebackancestors=nope":        false,
		"other=1,tracebackancestors=100": true,
		"tracebackancestors=100,tracebackancestors=0":    false,
		"tracebackancestors=0,tracebackancestors=100":    true,
		"tracebackancestors=100,other=1,another-setting": true,
	}

	for value, expected := range tests {
		if actual := tracebackAncestorsEnabled(value); actual != expected {
			t.Errorf("tracebackAncestorsEnabled(%q) = %t, want %t", value, actual, expected)
		}
	}
}

func TestTracesStringGroupsInEncounterOrder(t *testing.T) {
	first := &Trace{
		Raw:        "goroutine 1 [select]:\nfirst()\n\t/first.go:1",
		WaitReason: "select",
		Stacks:     []Stack{{Func: "first", Loc: "/first.go:1"}},
	}
	firstDuplicate := &Trace{
		Raw:        "goroutine 2 [select]:\nfirst()\n\t/first.go:1",
		WaitReason: "select",
		Stacks:     []Stack{{Func: "first", Loc: "/first.go:1"}},
	}
	second := &Trace{
		Raw:        "goroutine 3 [chan receive]:\nsecond()\n\t/second.go:2",
		WaitReason: "chan receive",
		Stacks:     []Stack{{Func: "second", Loc: "/second.go:2"}},
	}

	formatted := Traces{first, second, firstDuplicate}.String()
	if !strings.HasPrefix(formatted, "[2] "+first.Raw) {
		t.Fatalf("first group is not stable:\n%s", formatted)
	}
	if !strings.HasSuffix(formatted, second.Raw) {
		t.Fatalf("second group is not stable:\n%s", formatted)
	}
}
