package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// editSchema returns the fixture schema after edit changes its domains.
func editSchema(t *testing.T, edit func(domains map[string]map[string]any)) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	domains := map[string]map[string]any{}
	for _, domain := range schema["domains"].([]any) {
		domain := domain.(map[string]any)
		domains[domain["domain"].(string)] = domain
	}
	edit(domains)
	data, err = json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// find returns the entry of list whose key has value.
func find(list any, key, value string) map[string]any {
	for _, entry := range list.([]any) {
		if entry := entry.(map[string]any); entry[key] == value {
			return entry
		}
	}
	panic("fixture lacks " + value)
}

func readRecord(t *testing.T, data []byte) []string {
	t.Helper()
	var record compatibility
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record.Members
}

// olderFixture is the fixture as an older browser describes it: the
// Fixture.getValue result lacks value, Fixture.Node.id is optional, and the
// treeChanged event does not exist.
func olderFixture(t *testing.T) []byte {
	return editSchema(t, func(domains map[string]map[string]any) {
		fixture := domains["Fixture"]
		getValue := find(fixture["commands"], "name", "getValue")
		returns := getValue["returns"].([]any)
		getValue["returns"] = returns[1:]
		find(find(fixture["types"], "id", "Node")["properties"], "name", "id")["optional"] = true
		events := fixture["events"].([]any)
		fixture["events"] = events[:1]
	})
}

// Members that an older schema lacks or marks optional are not required, are
// documented as optional in older browsers, and are recorded; recorded members
// that the schema no longer has are dropped.
func TestCompatibilityRecord(t *testing.T) {
	data, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	files, err := render(data, compatibility{Members: []string{"Fixture.Gone.member", "Fixture.changed.options"}}, olderFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Fixture.Node.id", "Fixture.changed.options", "Fixture.getValueResult.value"}
	if got := readRecord(t, files[compatibilityOutput]); !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %v, want %v", got, want)
	}
	for name, fragments := range map[string][]string{
		decodersOutput: {
			`case "value": err = inField(decodeString(d, &m.Value, false), "value")`,
			`return missingMember(seen, "tree")`,
			`case "id": err = inField(decodeInt(d, &m.ID, false), "id")`,
			`func (m *FixtureChanged) decodeJSON(d *decoder) error { ok, err := d.object()`,
		},
		"fixture.go": {
			"// Value (optional in older browsers) ...",
			"// ID (optional in older browsers) ...",
			"// Options (optional in older browsers) ...",
		},
		"definitions_test.go": {"c := &Client{ret: json.RawMessage(`{\"tree\":{}}`)}"},
	} {
		code := strings.Join(strings.Fields(string(files[name])), " ")
		for _, fragment := range fragments {
			if !strings.Contains(code, strings.Join(strings.Fields(fragment), " ")) {
				t.Errorf("%s lacks %q", name, fragment)
			}
		}
	}
}

// An older schema leaves out "returns" for a command that returns nothing and
// "properties" for an object type without declared members. An older browser
// still answers such a command with an empty object and sends such a type
// without the members that a newer schema requires, so they are recorded.
func TestCompatibilityRecordEmptyDefinitions(t *testing.T) {
	older := editSchema(t, func(domains map[string]map[string]any) {
		fixture := domains["Fixture"]
		delete(find(fixture["commands"], "name", "getValue"), "returns")
		delete(find(fixture["types"], "id", "Node"), "properties")
	})
	data, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	files, err := render(data, compatibility{}, older)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Fixture.Node.id", "Fixture.getValueResult.extra", "Fixture.getValueResult.tree", "Fixture.getValueResult.value"}
	if got := readRecord(t, files[compatibilityOutput]); !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %v, want %v", got, want)
	}
}

// Generation compares the new schema with the previous one in the output
// directory and keeps the record, so members added by a newer browser stay
// optional for older ones in later generations.
func TestGenerateRecordsAddedMembers(t *testing.T) {
	dir := t.TempDir()
	generateSchema := func(data []byte, options GeneratorOptions) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "schema.json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		options.SchemaPath, options.OutputDir = path, dir
		if err := generate(t.Context(), options, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	record := func() []string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, compatibilityOutput))
		if err != nil {
			t.Fatal(err)
		}
		return readRecord(t, data)
	}
	older := olderFixture(t)
	generateSchema(older, GeneratorOptions{})
	if got := record(); len(got) != 0 {
		t.Fatalf("first record = %v", got)
	}
	current, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Fixture.Node.id", "Fixture.getValueResult.value"}
	generateSchema(current, GeneratorOptions{})
	if got := record(); !reflect.DeepEqual(got, want) {
		t.Fatalf("record after an update = %v, want %v", got, want)
	}
	generateSchema(current, GeneratorOptions{})
	if got := record(); !reflect.DeepEqual(got, want) {
		t.Fatalf("record after regeneration = %v, want %v", got, want)
	}
	generateSchema(current, GeneratorOptions{Check: true})

	// An explicit older schema adds the members it lacks.
	oldest := editSchema(t, func(domains map[string]map[string]any) {
		changed := find(domains["Fixture"]["events"], "name", "changed")
		changed["parameters"] = []any{}
	})
	path := filepath.Join(t.TempDir(), "oldest.json")
	if err := os.WriteFile(path, oldest, 0o644); err != nil {
		t.Fatal(err)
	}
	generateSchema(current, GeneratorOptions{CompatPath: path})
	want = []string{"Fixture.Node.id", "Fixture.changed.options", "Fixture.getValueResult.value"}
	if got := record(); !reflect.DeepEqual(got, want) {
		t.Fatalf("record with an older schema = %v, want %v", got, want)
	}
}

func TestTooManyRequiredMembers(t *testing.T) {
	data := editSchema(t, func(domains map[string]map[string]any) {
		changed := find(domains["Fixture"]["events"], "name", "changed")
		var parameters []any
		for i := range 65 {
			parameters = append(parameters, map[string]any{"name": "p" + strings.Repeat("x", i), "type": "string"})
		}
		changed["parameters"] = parameters
	})
	if _, err := render(data, compatibility{}); err == nil || !strings.Contains(err.Error(), "65 required members") {
		t.Fatalf("render = %v", err)
	}
}
