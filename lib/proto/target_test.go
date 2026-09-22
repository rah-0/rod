package proto_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/rah-0/rod/lib/proto"
)

type targetTabMetadataCase struct {
	targetID proto.TargetTargetID
	index    float64
	active   bool
	pinned   bool
	groupID  string
}

func TestTargetGetTargetsTabMetadata(t *testing.T) {
	data, err := os.ReadFile("testdata/target-tabs.json")
	if err != nil {
		t.Fatal(err)
	}
	var result proto.TargetGetTargetsResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.TargetInfos) != 3 {
		t.Fatalf("target count = %d, want 3", len(result.TargetInfos))
	}
	for i, want := range []targetTabMetadataCase{
		{targetID: "selected-tab", index: 0, active: true, pinned: true},
		{targetID: "background-tab", index: 1, active: false, pinned: false, groupID: "example-group"},
	} {
		t.Run(string(want.targetID), func(t *testing.T) {
			info := result.TargetInfos[i]
			if info.TargetID != want.targetID || info.Type != "tab" {
				t.Fatalf("target = %q (%q)", info.TargetID, info.Type)
			}
			for key, expected := range map[string]any{
				"tabStripIndex": want.index,
				"tabActive":     want.active,
				"tabPinned":     want.pinned,
			} {
				value, present := info.EmbedderData[key]
				if !present || value.Val() != expected {
					t.Errorf("%s = %v, present=%v; want %v", key, value.Val(), present, expected)
				}
			}
			group, present := info.EmbedderData["tabGroupId"]
			if present != (want.groupID != "") || present && group.Val() != want.groupID {
				t.Errorf("tabGroupId = %v, present=%v; want %q", group.Val(), present, want.groupID)
			}
		})
	}

	unknown, present := result.TargetInfos[0].EmbedderData["futureMetadata"]
	wantUnknown := map[string]any{
		"label": "preserved",
		"nested": map[string]any{
			"enabled": false,
			"values":  []any{float64(0), "item", nil},
		},
	}
	if !present || !reflect.DeepEqual(unknown.Val(), wantUnknown) {
		t.Fatalf("unknown metadata = %#v, present=%v", unknown.Val(), present)
	}
	legacy := result.TargetInfos[2]
	if legacy.TargetID != "legacy-tab" || legacy.EmbedderData != nil {
		t.Fatalf("older-browser target metadata = %+v", legacy)
	}
	if selected, present := legacy.EmbedderData["tabActive"]; present || !selected.IsZero() {
		t.Fatalf("older-browser target fabricated selection: %v, present=%v", selected, present)
	}

	roundtrip, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(roundtrip, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("tab metadata changed during roundtrip: %s", roundtrip)
	}
}
