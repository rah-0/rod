package guardian

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The supervisor's initializer has to run before the package initializers of
// the application that it is re-executed from. Go initializes the ready
// package whose import path sorts first. An import such as strings,
// encoding/json or os/exec depends on unicode, whose path sorts after nearly
// every other one: it lets every ready package that sorts before unicode
// initialize first, in the supervisor of every browser launch. The allowed
// packages are os, packages that os depends on, strconv, whose imports os
// depends on as well, and os/signal, which adds context to them and is ready
// as soon as os is. Test files are not checked: they are not part of an
// application.
func TestImports(t *testing.T) {
	allowed := map[string]bool{
		"errors": true, "io": true, "os": true, "os/signal": true, "path": true,
		"runtime": true, "strconv": true, "sync": true, "syscall": true, "time": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !allowed[path] {
				t.Errorf("%s imports %s, which delays the supervisor's initializer", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}
