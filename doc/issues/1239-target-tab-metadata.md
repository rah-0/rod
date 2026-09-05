# Preserve experimental tab metadata in TargetInfo

Priority: P2, bounded protocol enhancement. Source: [issue #1239](https://github.com/go-rod/rod/issues/1239).

Reviewed: 2026-09-05.

## Current evidence

The generated [TargetTargetInfo](../../lib/proto/target.go) has no `embedderData` field, so normal decoding discards it. The fork deliberately regenerates from a [pinned Chrome 128 schema](../../lib/proto/generate/README.md).

The current official [TargetInfo schema](https://chromedevtools.github.io/devtools-protocol/tot/Target/#type-TargetInfo) declares optional experimental `embedderData` for tab targets. Chromium [commit 5aa804a](https://chromium.googlesource.com/chromium/src/+/5aa804ae0b62bd1b0d54f57494211239e2ed5ffe) confirms Chrome provides tab-strip index, selected/pinned state, and optional group identity, with changes currently observed by polling. This confirms that the protocol model does not preserve the metadata.

## Scope

Add the verified optional metadata through the generator's documented source/patch path with explicit provenance, and regenerate affected output. Use a forward-compatible representation that preserves unknown embedder fields and distinguishes absent data from false/zero values. Provide a small example querying tab targets through the existing `TargetGetTargets` filter. A new `Browser.Tabs` hierarchy and page-to-tab mapping API are outside this task.

## Acceptance

- Decode a fixture containing tab metadata without losing known or unknown fields.
- Older-browser payloads without metadata remain supported and do not fabricate a selected tab.
- Generation stays deterministic/offline and preserves the existing protocol API; generated files are not hand-edited.
- The example reads state without activating tabs and explains selected-within-window versus OS focus, optional availability, and polling.
