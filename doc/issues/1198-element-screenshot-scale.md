# Crop element screenshots in screenshot pixel coordinates

Priority: P2. Sources: [issue #1198](https://github.com/go-rod/rod/issues/1198), duplicate [#1034](https://github.com/go-rod/rod/issues/1034), [PR #1200](https://github.com/go-rod/rod/pull/1200). See the existing [pull assessment](../pulls/1200-element-screenshot-scale.md).

Reviewed: 2026-09-05.

## Current evidence

[Element.Screenshot](../../element.go) captures a page bitmap, obtains an element's CSS box, and passes the box directly to [utils.CropImage](../../lib/utils/utils.go). Bitmap pixels and CSS coordinates differ when the effective scale is not one.

Reproduced in Chrome 152.0.7977.64 with device scale factor 2: a red element at CSS `(200,120)` sized `100×50` returned a white `100×50` crop. The expected region is red and `200×100` image pixels. Both position and dimensions are wrong.

## Scope

Convert bounds using the actual capture scale and origin, with a defined rounding policy. Do not rely only on `browser.defaultDevice`, which can differ from current per-page emulation or a browser attached through `ControlURL`. Preserve transformed-element behavior. The proposed upstream fix needs stronger coverage before adoption.

## Acceptance

- Positioned colored fixtures at scales 1, 1.5, and 2 yield correct pixel content and dimensions.
- Fractional coordinates preserve the complete element without an off-by-one edge loss.
- Scrolled pages, iframe elements, CSS transforms, and per-page emulation retain correct coordinates.
- PNG/JPEG behavior and operational error propagation remain intact.
