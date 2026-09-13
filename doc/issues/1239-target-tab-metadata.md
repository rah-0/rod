# Preserve experimental tab metadata in TargetInfo

Priority: P2, bounded protocol enhancement. Source: [issue #1239](https://github.com/go-rod/rod/issues/1239).

Reviewed: 2026-09-13.

## Current evidence

The generated [TargetTargetInfo](../../lib/proto/target.go) includes optional `EmbedderData` as `map[string]jsonvalue.Value`. [Generation from the installed browser](../../lib/proto/generate/README.md) preserves this field and unknown metadata keys; absent metadata remains nil.

The official [TargetInfo schema](https://chromedevtools.github.io/devtools-protocol/tot/Target/#type-TargetInfo) declares optional experimental `embedderData` for tab targets. Chromium [commit 5aa804a](https://chromium.googlesource.com/chromium/src/+/5aa804ae0b62bd1b0d54f57494211239e2ed5ffe) describes tab-strip index, selected/pinned state, and optional group identity, with changes observed by polling.

## Scope

The protocol representation is present. Remaining work is focused decoding coverage and a small example querying tab targets through the existing `TargetGetTargets` filter. A new `Browser.Tabs` hierarchy and page-to-tab mapping API are outside this task.

## Acceptance

- Decode a fixture containing tab metadata without losing known or unknown fields.
- Older-browser payloads without metadata remain supported and do not fabricate a selected tab.
- Generation reads the installed browser and supports deterministic reproduction with explicit saved schema input; generated files are not hand-edited.
- The example reads state without activating tabs and explains selected-within-window versus OS focus, optional availability, and polling.
