package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/rah-0/rod/lib/launcher"
)

const (
	schemaOutput     = "generate/schema.json"
	provenanceOutput = "generate/schema-provenance.json"
	maxProtocolBytes = 32 << 20
)

type GeneratorOptions struct {
	SchemaPath string
	// CompatPath is the protocol schema of an older browser whose missing
	// members are recorded in schema-compatibility.json.
	CompatPath string
	OutputDir  string
	Bin        string
	NoSandbox  bool
	Check      bool
	Timeout    time.Duration
}

type BrowserVersion struct {
	Browser         string `json:"Browser"`
	ProtocolVersion string `json:"Protocol-Version"`
	UserAgent       string `json:"User-Agent"`
	V8Version       string `json:"V8-Version"`
	WebKitVersion   string `json:"WebKit-Version"`
}

type SchemaProvenance struct {
	Source  string          `json:"source"`
	SHA256  string          `json:"sha256"`
	Browser *BrowserVersion `json:"browser,omitempty"`
}

type ProtocolSource struct {
	Data       []byte
	Provenance SchemaProvenance
}

type browserLauncher interface {
	LaunchNew(context.Context) (string, error)
	Kill()
	CleanupContext(context.Context) error
}

func generate(ctx context.Context, options GeneratorOptions, output io.Writer) error {
	var source ProtocolSource
	var err error
	if options.SchemaPath == "" {
		if options.Timeout <= 0 {
			return fmt.Errorf("browser timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(ctx, options.Timeout)
		defer cancel()
		source, err = installedProtocol(ctx, options)
	} else {
		source.Data, err = os.ReadFile(options.SchemaPath)
		source.Provenance.Source = "explicit schema file"
	}
	if err != nil {
		return err
	}
	if source.Provenance.Browser != nil {
		fmt.Fprintf(output, "Protocol source: %s (CDP %s)\n", source.Provenance.Browser.Browser, source.Provenance.Browser.ProtocolVersion)
	} else {
		fmt.Fprintf(output, "Protocol source: %s\n", options.SchemaPath)
	}
	source.Data, err = normalizeSchema(source.Data)
	if err != nil {
		return err
	}
	compat, older, err := compatibilityInputs(options)
	if err != nil {
		return err
	}
	files, err := render(source.Data, compat, older...)
	if err != nil {
		return err
	}
	if options.Check {
		if err := checkOutputs(options.OutputDir, files); err != nil {
			return err
		}
		fmt.Fprintln(output, "Generated protocol bindings are current.")
		return nil
	}
	source.Provenance.SHA256 = fmt.Sprintf("%x", sha256.Sum256(source.Data))
	provenance, err := json.MarshalIndent(source.Provenance, "", "  ")
	if err != nil {
		return err
	}
	files[schemaOutput] = source.Data
	files[provenanceOutput] = append(provenance, '\n')
	if err := replaceOutputs(options.OutputDir, files); err != nil {
		return err
	}
	fmt.Fprintf(output, "Generated protocol bindings in %s.\n", options.OutputDir)
	return nil
}

// compatibilityInputs reads the compatibility record and the previous schema
// from the output directory, and the older schema that options selects. The
// previous schema lets generation record the members that the new schema adds.
func compatibilityInputs(options GeneratorOptions) (compatibility, [][]byte, error) {
	var compat compatibility
	var older [][]byte
	record, err := os.ReadFile(filepath.Join(options.OutputDir, compatibilityOutput))
	if err == nil {
		if err := json.Unmarshal(record, &compat); err != nil {
			return compat, nil, fmt.Errorf("decode %s: %w", compatibilityOutput, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return compat, nil, err
	}
	previous, err := os.ReadFile(filepath.Join(options.OutputDir, schemaOutput))
	if err == nil {
		older = append(older, previous)
	} else if !errors.Is(err, os.ErrNotExist) {
		return compat, nil, err
	}
	if options.CompatPath != "" {
		data, err := os.ReadFile(options.CompatPath)
		if err != nil {
			return compat, nil, err
		}
		older = append(older, data)
	}
	return compat, older, nil
}

// Canonical JSON makes hashes independent of endpoint whitespace and object key order.
func normalizeSchema(data []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode protocol schema: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func installedProtocol(ctx context.Context, options GeneratorOptions) (ProtocolSource, error) {
	l := launcher.New().RemoteDebuggingPort(0).
		Headless(true).Devtools(false).NoSandbox(options.NoSandbox)
	if options.Bin != "" {
		l.Bin(options.Bin)
	}
	client := &http.Client{
		Timeout: options.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	source, err := captureProtocol(ctx, l, client)
	if err != nil && l.Output() != "" {
		return ProtocolSource{}, fmt.Errorf("%w\nbrowser output: %s", err, l.Output())
	}
	return source, err
}

func captureProtocol(ctx context.Context, l browserLauncher, client *http.Client) (source ProtocolSource, err error) {
	defer func() {
		l.Kill()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, l.CleanupContext(cleanup))
	}()
	endpoint, err := l.LaunchNew(ctx)
	if err != nil {
		return source, fmt.Errorf("launch installed browser: %w", err)
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return source, err
	}
	switch base.Scheme {
	case "ws":
		base.Scheme = "http"
	case "wss":
		base.Scheme = "https"
	default:
		return source, fmt.Errorf("unexpected browser endpoint scheme %q", base.Scheme)
	}
	base.RawQuery, base.Fragment, base.RawPath = "", "", ""
	base.Path = "/json/version"
	versionData, err := fetchProtocolJSON(ctx, client, base.String())
	if err != nil {
		return source, err
	}
	var version BrowserVersion
	if err := json.Unmarshal(versionData, &version); err != nil {
		return source, fmt.Errorf("decode browser version: %w", err)
	}
	if version.Browser == "" || version.ProtocolVersion == "" {
		return source, fmt.Errorf("browser version must include Browser and Protocol-Version")
	}
	base.Path = "/json/protocol"
	source.Data, err = fetchProtocolJSON(ctx, client, base.String())
	source.Provenance = SchemaProvenance{Source: "installed browser /json/protocol", Browser: &version}
	return source, err
}

func fetchProtocolJSON(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %s", endpoint, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxProtocolBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", endpoint, err)
	}
	if len(data) > maxProtocolBytes {
		return nil, fmt.Errorf("protocol response exceeds %d bytes", maxProtocolBytes)
	}
	return data, nil
}
