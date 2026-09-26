package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestRenderFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := render(data, compatibility{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := render(data, compatibility{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("generation is nondeterministic")
	}
	for name, code := range first {
		if name == compatibilityOutput {
			continue
		}
		formatted, err := format.Source(code)
		if err != nil || !bytes.Equal(code, formatted) {
			t.Fatalf("unformatted %s: %v", name, err)
		}
		if !owned(code) {
			t.Fatalf("missing ownership marker: %s", name)
		}
	}
	checks := map[string][]string{
		"fixture.go":          {"Value jsonvalue.Value `json:\"value\"`", "Enabled *bool `json:\"enabled,omitempty\"`", "Enabled bool `json:\"enabled,omitempty\"`", "Count *int `json:\"count,omitempty\"`", "Ratio *float64 `json:\"ratio,omitempty\"`", "Options *FixtureOptions `json:\"options,omitempty\"`", "Deprecated: This protocol API is deprecated."},
		"fetch.go":            {"Body []byte `json:\"body\"`"},
		"input.go":            {"DeltaX float64 `json:\"deltaX\"`", "DeltaY float64 `json:\"deltaY\"`"},
		"network.go":          {"Expires TimeSinceEpoch `json:\"expires\"`", "Urls []string `json:\"urls,omitzero\"`"},
		"target.go":           {"TargetTargetInfoTypeBackgroundPage", "EmbedderData map[string]jsonvalue.Value `json:\"embedderData,omitempty\"`"},
		"page.go":             {"PageLifecycleEventNameDOMContentLoaded"},
		"definitions.go":      {"reflect.TypeFor[FixtureGetValue]()"},
		"definitions_test.go": {"func TestFixtureGetValue(t *testing.T)", "c.methodName != \"Fixture.getValue\"", "reflect.DeepEqual(c.params, request)", "got != \"Fixture.changed\""},
	}
	// Command tests answer with the smallest result that has every required member.
	checks["definitions_test.go"] = append(checks["definitions_test.go"], "\"encoding/json\"", "c := &Client{ret: json.RawMessage(`{\"value\":\"\",\"tree\":{\"id\":0}}`)}")
	checks[decodersOutput] = []string{
		// Required members of every kind are recorded as seen and reported
		// when missing; optional members are decoded when present.
		`func (m *FixtureGetValueResult) decodeJSON(d *decoder) error { var seen uint64 ok, err := d.object() for ok && err == nil { var name []byte if name, ok, err = d.member(); !ok || err != nil { break } switch string(name) { ` +
			`case "value": seen |= 1 << 0 err = inField(decodeString(d, &m.Value, true), "value") ` +
			`case "enabled": err = inField(decodeBool(d, &m.Enabled, false), "enabled") ` +
			`case "tree": seen |= 1 << 1 err = inField(decodeObject(d, &m.Tree, true), "tree") ` +
			// Experimental and deprecated members are not required.
			`case "extra": err = inField(decodeObject(d, &m.Extra, false), "extra") ` +
			`default: err = d.skip() } } if err != nil { return err } if seen != 1<<2-1 && !d.lenient { return missingMember(seen, "value tree") } return nil }`,
		`case "nodes": seen |= 1 << 1 err = inField(decodeList(d, &m.Nodes, true, decodeObject[FixtureNode]), "nodes")`,
		`case "preview": err = inField(decodeObject(d, &m.Preview, false), "preview") case "legacy": err = inField(decodeObject(d, &m.Legacy, false), "legacy")`,
		`return missingMember(seen, "root nodes options")`,
		// Optional numbers are pointers.
		`case "count": err = inField(decodePointer(d, &m.Count, decodeInt[int]), "count")`,
		`case "value": err = inField(decodeValue(d, &m.Value, false), "value")`,
		// Structs without required members return the decoding error.
		`func (m *FixtureOptions) decodeJSON(d *decoder) error { ok, err := d.object()`,
		`default: err = d.skip() } } return err }`,
	}
	checks[compatibilityOutput] = []string{`{ "members": [] }`}
	for name, expected := range checks {
		code := strings.Join(strings.Fields(string(first[name])), " ")
		for _, fragment := range expected {
			if !strings.Contains(code, fragment) {
				t.Errorf("%s lacks %q", name, fragment)
			}
		}
	}
	// Only command results, events and the objects they contain are decoded
	// from browser data. Command parameters and types that only they use are not.
	for _, name := range []string{"FixtureLeaf", "FixtureGetValue", "TargetTargetInfo", "InputDispatchMouseEvent"} {
		if bytes.Contains(first[decodersOutput], []byte("func (m *"+name+") decodeJSON(")) {
			t.Errorf("%s has an unnecessary decoder", name)
		}
	}
}

func TestRequiredObjectCycle(t *testing.T) {
	data, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	// A required object that eventually requires itself has no finite value.
	parent := regexp.MustCompile(`("name": "parent",\s*"\$ref": "Node"),\s*"optional": true`)
	cycle := parent.ReplaceAll(data, []byte("$1"))
	if bytes.Equal(cycle, data) {
		t.Fatal("fixture changed")
	}
	if _, err := render(cycle, compatibility{}); err == nil || !strings.Contains(err.Error(), "required object cycle through FixtureNode") {
		t.Fatalf("render = %v", err)
	}
}

func TestGenerationFailurePreservesOutputs(t *testing.T) {
	fixture, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	invalidGo := strings.Replace(string(fixture), `"Fixture"`, `"Bad-Name"`, 1)
	for index, schema := range []string{"{", `{ "version": {"major":"1","minor":"3"}, "domains": [{"domain":"MissingPatchTargets"}] }`, invalidGo} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			dir := t.TempDir()
			original := []byte(generatedHeader + "\npackage proto\n")
			for _, name := range []string{"previous.go", "handwritten.go"} {
				if err := os.WriteFile(filepath.Join(dir, name), original, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "schema.json")
			if err := os.WriteFile(path, []byte(schema), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := generate(t.Context(), GeneratorOptions{SchemaPath: path, OutputDir: dir}, io.Discard); err == nil {
				t.Fatal("invalid schema accepted")
			}
			for _, name := range []string{"previous.go", "handwritten.go"} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || !bytes.Equal(data, original) {
					t.Fatalf("damaged %s: %v", name, err)
				}
			}
		})
	}
}

func TestReplaceOnlyOwnedOutputs(t *testing.T) {
	dir := t.TempDir()
	handwritten := []byte("package proto\n// handwritten, without an a_ prefix\n")
	old := []byte("// This file is generated by \"./lib/proto/generate\"\npackage proto\n")
	for name, data := range map[string][]byte{"custom.go": handwritten, "obsolete.go": old, "current.go": old} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	newCode := []byte(generatedHeader + "\npackage proto\nconst Updated = true\n")
	if err := replaceOutputs(dir, map[string][]byte{"current.go": newCode}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "obsolete.go")); !os.IsNotExist(err) {
		t.Fatalf("obsolete output exists: %v", err)
	}
	for name, expected := range map[string][]byte{"custom.go": handwritten, "current.go": newCode} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(data, expected) {
			t.Fatalf("unexpected %s: %v", name, err)
		}
	}
	if err := replaceOutputs(dir, map[string][]byte{"custom.go": newCode}); err == nil {
		t.Fatal("handwritten collision accepted")
	}
	data, err := os.ReadFile(filepath.Join(dir, "current.go"))
	if err != nil || !bytes.Equal(data, newCode) {
		t.Fatalf("collision damaged existing output: %v", err)
	}
}

func TestOwnedHeaders(t *testing.T) {
	for _, header := range []string{generatedHeader, "// This file is generated by \"./lib/proto/generate\""} {
		for _, newline := range []string{"\n", "\r\n"} {
			if !owned([]byte(header + newline + "package proto")) {
				t.Errorf("unrecognized header %q", header+newline)
			}
		}
	}
	if owned([]byte("// Code generated by another generator; DO NOT EDIT.\npackage proto")) {
		t.Fatal("claimed another generator's output")
	}
}

func TestSnapshotProvenance(t *testing.T) {
	data, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("schema-provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var provenance SchemaProvenance
	if err := json.Unmarshal(raw, &provenance); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(data); provenance.SHA256 != fmt.Sprintf("%x", sum) {
		t.Fatal("schema does not match provenance checksum")
	}
}

func TestRollbackFailureRetainsOriginal(t *testing.T) {
	dir := t.TempDir()
	original := []byte(generatedHeader + "\npackage proto\nconst Original = true\n")
	if err := os.WriteFile(filepath.Join(dir, "current.go"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected rename failure")
	rename := func(oldPath, newPath string) error {
		if filepath.Dir(newPath) == dir {
			return failure
		}
		return os.Rename(oldPath, newPath)
	}
	err := replaceOutputsWithRename(dir, map[string][]byte{"current.go": []byte(generatedHeader + "\npackage proto\n")}, rename)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "original retained") {
		t.Fatalf("missing recovery error: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".proto-generate-*", "backup", "current.go"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing backup: %v %v", backups, err)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("original data lost: %v", err)
	}
}

func TestInstallationFailureRestoresOutputs(t *testing.T) {
	dir := t.TempDir()
	original := []byte(generatedHeader + "\npackage proto\nconst Original = true\n")
	for _, name := range []string{"current.go", "obsolete.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), original, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("injected installation failure")
	rename := func(oldPath, newPath string) error {
		if filepath.Dir(newPath) == dir && filepath.Base(newPath) == "current.go" && filepath.Base(filepath.Dir(oldPath)) != "backup" {
			return failure
		}
		return os.Rename(oldPath, newPath)
	}
	newCode := []byte(generatedHeader + "\npackage proto\n")
	err := replaceOutputsWithRename(dir, map[string][]byte{"added.go": newCode, "current.go": newCode}, rename)
	if !errors.Is(err, failure) {
		t.Fatalf("installation failure = %v", err)
	}
	for _, name := range []string{"current.go", "obsolete.go"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(data, original) {
			t.Fatalf("failed installation damaged %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "added.go")); !os.IsNotExist(err) {
		t.Fatalf("failed installation retained new output: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("rollback left temporary files: %v %v", entries, err)
	}
}

func TestReservedOutputNames(t *testing.T) {
	data, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"Definitions", "DefinitionsTest", "Decoders"} {
		t.Run(domain, func(t *testing.T) {
			fixture := bytes.Replace(data, []byte(`"Fixture"`), []byte(`"`+domain+`"`), 1)
			if _, err := render(fixture, compatibility{}); err == nil || !strings.Contains(err.Error(), "duplicate output") {
				t.Fatalf("reserved output accepted: %v", err)
			}
		})
	}
}
