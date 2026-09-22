package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	var result proto.TargetGetTargetsResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.TargetInfos) == 0 {
		t.Fatal("the example's blank tab was not reported")
	}
	for _, info := range result.TargetInfos {
		if info.Type != "tab" || info.TargetID == "" || info.URL != "about:blank" {
			t.Fatalf("unexpected tab target: %+v", info)
		}
		// Metadata is optional, including on browsers that support tab targets.
		if active, ok := info.EmbedderData["tabActive"]; ok {
			if _, ok := active.Val().(bool); !ok {
				t.Fatalf("tabActive is not a boolean: %s", active)
			}
		}
	}
}

type tabQueryClient struct {
	t     *testing.T
	calls int
	err   error
}

func (c *tabQueryClient) Call(_ context.Context, session, method string, params any) ([]byte, error) {
	c.t.Helper()
	c.calls++
	if session != "" || method != "Target.getTargets" {
		c.t.Fatalf("unexpected command: session=%q method=%q", session, method)
	}
	want := proto.TargetGetTargets{Filter: proto.TargetTargetFilter{{Type: "tab"}, {Exclude: true}}}
	if !reflect.DeepEqual(params, want) {
		c.t.Fatalf("query = %#v, want only tab targets", params)
	}
	return []byte(`{"targetInfos":[{"targetId":"legacy","type":"tab","title":"","url":"about:blank","attached":false,"canAccessOpener":false}]}`), c.err
}

func TestWriteTabsReadOnly(t *testing.T) {
	client := &tabQueryClient{t: t}
	var output bytes.Buffer
	if err := writeTabs(client, &output); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("query sent %d commands, want one", client.calls)
	}
	if bytes.Contains(output.Bytes(), []byte("embedderData")) || bytes.Contains(output.Bytes(), []byte("tabActive")) {
		t.Fatalf("fabricated metadata for an older browser: %s", &output)
	}
	client.err = errors.New("query failed")
	output.Reset()
	if err := writeTabs(client, &output); !errors.Is(err, client.err) || output.Len() != 0 {
		t.Fatalf("failed query: error=%v output=%q", err, &output)
	}
}
