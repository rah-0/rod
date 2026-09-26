package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type outputSnapshot struct {
	Data    string
	ModTime time.Time
}

type captureCase struct {
	Name       string
	Version    string
	Status     int
	LaunchErr  error
	CleanupErr error
	WantError  string
}

type fakeBrowserLauncher struct {
	Endpoint   string
	LaunchErr  error
	CleanupErr error
	Calls      []string
}

func (l *fakeBrowserLauncher) LaunchNew(context.Context) (string, error) {
	l.Calls = append(l.Calls, "launch")
	return l.Endpoint, l.LaunchErr
}

func (l *fakeBrowserLauncher) Kill() { l.Calls = append(l.Calls, "kill") }

func (l *fakeBrowserLauncher) CleanupContext(ctx context.Context) error {
	l.Calls = append(l.Calls, "cleanup")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return l.CleanupErr
}

func snapshotOutputs(t *testing.T, dir string) map[string]outputSnapshot {
	t.Helper()
	outputs := make(map[string]outputSnapshot)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err == nil {
			outputs[path] = outputSnapshot{Data: string(data), ModTime: info.ModTime()}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return outputs
}

func TestGenerateOfflineAndCheck(t *testing.T) {
	dir := t.TempDir()
	options := GeneratorOptions{SchemaPath: "testdata/schema.json", OutputDir: dir}
	if err := generate(t.Context(), options, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, schemaOutput))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, provenanceOutput))
	if err != nil {
		t.Fatal(err)
	}
	var provenance SchemaProvenance
	if err := json.Unmarshal(raw, &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance.Browser != nil || provenance.Source != "explicit schema file" || provenance.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatalf("unexpected provenance: %+v", provenance)
	}
	options.Check = true
	before := snapshotOutputs(t, dir)
	var output bytes.Buffer
	if err := generate(t.Context(), options, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "bindings are current") {
		t.Fatalf("missing check result: %s", &output)
	}
	if !reflect.DeepEqual(before, snapshotOutputs(t, dir)) {
		t.Fatal("check modified outputs")
	}

	if err := os.Remove(filepath.Join(dir, "fixture.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "obsolete.go"), []byte(generatedHeader+"\npackage proto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom.go"), []byte("package proto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before = snapshotOutputs(t, dir)
	err = generate(t.Context(), options, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "fixture.go (missing)") || !strings.Contains(err.Error(), "obsolete.go (obsolete)") || strings.Contains(err.Error(), "custom.go") {
		t.Fatalf("unexpected stale check: %v", err)
	}
	if !reflect.DeepEqual(before, snapshotOutputs(t, dir)) {
		t.Fatal("failed check modified outputs")
	}
}

func TestCheckProtocolChangesAndNormalization(t *testing.T) {
	data, err := os.ReadFile("testdata/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	files, err := render(data, compatibility{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := replaceOutputs(dir, files); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		t.Fatal(err)
	}
	equivalent, err := render(compact.Bytes(), compatibility{})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOutputs(dir, equivalent); err != nil {
		t.Fatalf("whitespace caused drift: %v", err)
	}
	changed := bytes.Replace(data, []byte(`"getValue"`), []byte(`"getNewValue"`), 1)
	if bytes.Equal(data, changed) {
		t.Fatal("fixture command not found")
	}
	newFiles, err := render(changed, compatibility{})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotOutputs(t, dir)
	if err := checkOutputs(dir, newFiles); err == nil || !strings.Contains(err.Error(), "fixture.go (changed)") {
		t.Fatalf("protocol change was not detected: %v", err)
	}
	if !reflect.DeepEqual(before, snapshotOutputs(t, dir)) {
		t.Fatal("stale check modified outputs")
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if err := checkOutputs(missing, files); err == nil {
		t.Fatal("missing outputs accepted")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check created missing output directory: %v", err)
	}
}

func TestMetadataReplacementRollback(t *testing.T) {
	dir := t.TempDir()
	original := map[string][]byte{
		"current.go":     []byte(generatedHeader + "\npackage proto\n"),
		schemaOutput:     []byte("original schema"),
		provenanceOutput: []byte("original provenance"),
	}
	if err := replaceOutputs(dir, original); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(dir, "generate", "README.md")
	if err := os.WriteFile(readme, []byte("handwritten documentation"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotOutputs(t, dir)
	failure := errors.New("injected metadata installation failure")
	rename := func(oldPath, newPath string) error {
		if newPath == filepath.Join(dir, schemaOutput) && !strings.Contains(oldPath, string(filepath.Separator)+"backup"+string(filepath.Separator)) {
			return failure
		}
		return os.Rename(oldPath, newPath)
	}
	files := map[string][]byte{
		"current.go":     []byte(generatedHeader + "\npackage proto\nconst Changed = true\n"),
		schemaOutput:     []byte("new schema"),
		provenanceOutput: []byte("new provenance"),
	}
	if err := replaceOutputsWithRename(dir, files, rename); !errors.Is(err, failure) {
		t.Fatalf("metadata installation error = %v", err)
	}
	if !reflect.DeepEqual(before, snapshotOutputs(t, dir)) {
		t.Fatal("metadata failure did not restore original output set")
	}
	if err := replaceOutputs(dir, files); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(readme)
	if err != nil || string(data) != "handwritten documentation" {
		t.Fatalf("metadata generation damaged handwritten documentation: %v", err)
	}
}

func TestCaptureProtocolAndCleanup(t *testing.T) {
	version := `{"Browser":"Chrome/152.0.0.0","Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://private-endpoint"}`
	failure := errors.New("injected failure")
	cases := []captureCase{
		{Name: "success", Version: version, Status: http.StatusOK},
		{Name: "fetch", Version: version, Status: http.StatusServiceUnavailable, WantError: "503"},
		{Name: "invalid version", Version: "{", WantError: "decode browser version"},
		{Name: "missing version", Version: `{}`, WantError: "must include"},
		{Name: "launch", LaunchErr: failure, WantError: "launch installed browser"},
		{Name: "cleanup", Version: version, Status: http.StatusOK, CleanupErr: failure, WantError: "injected failure"},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/json/version":
					_, _ = io.WriteString(w, tc.Version)
				case "/json/protocol":
					w.WriteHeader(tc.Status)
					_, _ = io.WriteString(w, `{"domains":[]}`)
				default:
					t.Errorf("unexpected endpoint: %s", r.URL)
				}
			}))
			defer server.Close()
			l := &fakeBrowserLauncher{Endpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test?ignored=true", LaunchErr: tc.LaunchErr, CleanupErr: tc.CleanupErr}
			source, err := captureProtocol(t.Context(), l, server.Client())
			if tc.WantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				if string(source.Data) != `{"domains":[]}` || source.Provenance.Browser.Browser != "Chrome/152.0.0.0" {
					t.Fatalf("unexpected capture: %+v", source)
				}
				metadata, err := json.Marshal(source.Provenance)
				if err != nil || bytes.Contains(metadata, []byte("private-endpoint")) {
					t.Fatalf("unsafe metadata: %s %v", metadata, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.WantError) {
				t.Fatalf("capture error = %v, want %s", err, tc.WantError)
			}
			if !reflect.DeepEqual(l.Calls, []string{"launch", "kill", "cleanup"}) {
				t.Fatalf("browser lifecycle = %v", l.Calls)
			}
			if tc.CleanupErr != nil && !errors.Is(err, tc.CleanupErr) {
				t.Fatalf("cleanup error was lost: %v", err)
			}
		})
	}
}

func TestCaptureProtocolCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l := &fakeBrowserLauncher{Endpoint: "ws://127.0.0.1:1/devtools/browser/test"}
	_, err := captureProtocol(ctx, l, &http.Client{Timeout: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture = %v", err)
	}
	if !reflect.DeepEqual(l.Calls, []string{"launch", "kill", "cleanup"}) {
		t.Fatalf("canceled browser lifecycle = %v", l.Calls)
	}
}
