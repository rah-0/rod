// Package goroutines captures and filters runtime goroutine stack traces.
package goroutines

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Stack is one function and source location in a goroutine trace.
type Stack struct {
	Func string
	Loc  string
}

// Trace describes one goroutine from a runtime stack snapshot.
type Trace struct {
	Raw                  string
	GoroutineID          int64
	GoroutineAncestorIDs []int64
	WaitReason           string
	Stacks               []Stack
}

// String returns the original runtime trace.
func (t Trace) String() string {
	return t.Raw
}

// HasParent reports whether id appears in the recorded ancestor chain.
func (t Trace) HasParent(id int64) bool {
	return slices.Contains(t.GoroutineAncestorIDs, id)
}

// Traces is a goroutine stack snapshot.
type Traces []*Trace

// Any reports whether the snapshot contains at least one trace.
func (traces Traces) Any() bool {
	return len(traces) != 0
}

// Filter returns traces that are not matched by ignore.
func (traces Traces) Filter(ignore Ignore) Traces {
	remaining := make(Traces, 0, len(traces))
	for _, trace := range traces {
		if ignore != nil && ignore(trace) {
			continue
		}
		remaining = append(remaining, trace)
	}
	return remaining
}

// String groups equivalent stacks and formats them for diagnostics.
func (traces Traces) String() string {
	type traceGroup struct {
		count int
		trace *Trace
	}

	groups := make([]traceGroup, 0, len(traces))
	indices := make(map[string]int, len(traces))
	for _, trace := range traces {
		if trace == nil {
			continue
		}

		key := trace.groupKey()
		if index, exists := indices[key]; exists {
			groups[index].count++
			continue
		}

		indices[key] = len(groups)
		groups = append(groups, traceGroup{count: 1, trace: trace})
	}

	var output strings.Builder
	for index, group := range groups {
		if index != 0 {
			output.WriteString("\n\n")
		}
		if group.count > 1 {
			_, _ = fmt.Fprintf(&output, "[%d] ", group.count)
		}
		output.WriteString(group.trace.Raw)
	}
	return output.String()
}

func (t *Trace) groupKey() string {
	if t.WaitReason == "" && len(t.Stacks) == 0 {
		return "raw\x00" + t.Raw
	}

	var key strings.Builder
	key.WriteString(t.WaitReason)
	for _, stack := range t.Stacks {
		key.WriteByte(0)
		key.WriteString(stack.Func)
		key.WriteByte(0)
		key.WriteString(stack.Loc)
	}
	return key.String()
}

// Snapshot captures the calling goroutine. When all is true, it also captures
// every other goroutine visible to the runtime.
func Snapshot(all bool) Traces {
	const initialBufferSize = 64 << 10

	bufferSize := initialBufferSize
	for {
		buffer := make([]byte, bufferSize)
		written := runtime.Stack(buffer, all)
		if written < len(buffer) {
			return parseTraces(string(buffer[:written]))
		}
		bufferSize *= 2
	}
}

func parseTraces(raw string) Traces {
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return nil
	}

	blocks := strings.Split(raw, "\n\n")
	traces := make(Traces, 0, len(blocks))
	for _, block := range blocks {
		block = strings.Trim(block, "\n")
		if block == "" {
			continue
		}
		traces = append(traces, parseTrace(block))
	}
	return traces
}

func parseTrace(raw string) *Trace {
	trace := &Trace{Raw: raw}
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return trace
	}

	if id, reason, ok := parseHeader(lines[0]); ok {
		trace.GoroutineID = id
		trace.WaitReason = reason
	}

	inAncestor := false
	for index := 1; index < len(lines); {
		line := lines[index]
		if ancestorID, ok := parseAncestor(line); ok {
			trace.GoroutineAncestorIDs = append(trace.GoroutineAncestorIDs, ancestorID)
			inAncestor = true
			index++
			continue
		}
		if inAncestor || line == "" || strings.HasPrefix(line, "...") {
			index++
			continue
		}
		if index+1 >= len(lines) {
			index++
			continue
		}

		function, functionOK := parseFunction(line)
		location, locationOK := parseLocation(lines[index+1])
		if !functionOK || !locationOK {
			index++
			continue
		}

		trace.Stacks = append(trace.Stacks, Stack{Func: function, Loc: location})
		index += 2
	}

	return trace
}

func parseHeader(line string) (int64, string, bool) {
	const prefix = "goroutine "
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "]:") {
		return 0, "", false
	}

	remainder := strings.TrimPrefix(line, prefix)
	idEnd := strings.IndexByte(remainder, ' ')
	reasonStart := strings.LastIndex(remainder, " [")
	if idEnd <= 0 || reasonStart < idEnd {
		return 0, "", false
	}

	id, err := strconv.ParseInt(remainder[:idEnd], 10, 64)
	if err != nil {
		return 0, "", false
	}

	reason := remainder[reasonStart+2 : len(remainder)-2]
	if reason == "" {
		return 0, "", false
	}
	return id, reason, true
}

func parseAncestor(line string) (int64, bool) {
	const (
		prefix = "[originating from goroutine "
		suffix = "]:"
	)
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return 0, false
	}

	id, err := strconv.ParseInt(line[len(prefix):len(line)-len(suffix)], 10, 64)
	return id, err == nil
}

func parseFunction(line string) (string, bool) {
	if function, ok := strings.CutPrefix(line, "created by "); ok {
		if marker := strings.LastIndex(function, " in goroutine "); marker >= 0 {
			function = function[:marker]
		}
		function = strings.TrimSpace(function)
		return function, function != ""
	}

	argumentStart := strings.LastIndexByte(line, '(')
	if argumentStart <= 0 {
		return "", false
	}
	function := strings.TrimSpace(line[:argumentStart])
	return function, function != ""
}

func parseLocation(line string) (string, bool) {
	if !strings.HasPrefix(line, "\t") {
		return "", false
	}

	location := strings.TrimPrefix(line, "\t")
	if offset := strings.LastIndex(location, " +0x"); offset >= 0 {
		location = location[:offset]
	}
	location = strings.TrimSpace(location)
	return location, location != ""
}

// Ignore reports whether a trace should be omitted from diagnostics.
type Ignore func(*Trace) bool

// Current ignores every goroutine present when Current is called.
func Current() Ignore {
	ids := make(map[int64]struct{})
	for _, trace := range Snapshot(true) {
		if trace != nil {
			ids[trace.GoroutineID] = struct{}{}
		}
	}

	return func(trace *Trace) bool {
		if trace == nil {
			return false
		}
		_, exists := ids[trace.GoroutineID]
		return exists
	}
}

// NonChildren ignores goroutines that are not descendants of the caller.
// Runtime ancestry must be enabled with GODEBUG=tracebackancestors=N.
func NonChildren() Ignore {
	if !tracebackAncestorsEnabled(os.Getenv("GODEBUG")) {
		panic(`goroutines: GODEBUG must include "tracebackancestors=N" with N greater than zero`)
	}

	current := Snapshot(false)
	if len(current) == 0 || current[0] == nil || current[0].GoroutineID == 0 {
		panic("goroutines: unable to identify the current goroutine")
	}
	return nonChildren(current[0].GoroutineID)
}

func nonChildren(parentID int64) Ignore {
	return func(trace *Trace) bool {
		return trace == nil || !trace.HasParent(parentID)
	}
}

func tracebackAncestorsEnabled(godebug string) bool {
	enabled := false
	for setting := range strings.SplitSeq(godebug, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(setting), "=")
		if !ok || name != "tracebackancestors" {
			continue
		}
		depth, err := strconv.Atoi(value)
		enabled = err == nil && depth > 0
	}
	return enabled
}

// Functions ignores traces whose first stack frame exactly matches a name.
func Functions(names ...string) Ignore {
	functions := make(map[string]struct{}, len(names))
	for _, name := range names {
		functions[name] = struct{}{}
	}

	return func(trace *Trace) bool {
		if trace == nil || len(trace.Stacks) == 0 {
			return false
		}
		_, exists := functions[trace.Stacks[0].Func]
		return exists
	}
}

// Combine joins ignore rules. A trace is ignored when any rule matches it.
func Combine(ignores ...Ignore) Ignore {
	return func(trace *Trace) bool {
		for _, ignore := range ignores {
			if ignore != nil && ignore(trace) {
				return true
			}
		}
		return false
	}
}
