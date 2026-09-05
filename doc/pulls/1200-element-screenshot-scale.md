# Crop element screenshots in image pixels

Priority: P2.

Source: upstream [#1200](https://github.com/go-rod/rod/pull/1200), associated with [#1198](https://github.com/go-rod/rod/issues/1198).

## Current evidence

[Element.Screenshot](../../element.go) captures the page, obtains an element box, and passes its coordinates directly to [utils.CropImage](../../lib/utils/utils.go). The box uses CSS coordinates; the captured image can use a different pixel scale. Existing [element screenshot coverage](../../element_test.go) checks only the default scale.

Reproduction with `DeviceScaleFactor: 2` in Chrome: a red element positioned at CSS `(100,100)` with size `100×40` produced a white `100×40` crop. Its image region should have been red and `200×80`. Both crop position and dimensions are wrong.

## Change

Convert the element bounds to screenshot coordinates before cropping. Establish the actual scale and coordinate origin for the capture rather than assuming CSS pixels equal image pixels. The upstream multiplication addresses the reproduced case, but its JavaScript `window.devicePixelRatio` read is page-modifiable and the patch supplies no regression tests. Validate the chosen source of scale, rounding, and iframe behavior before adopting it. Preserve the existing handling of CSS-transformed elements.

## Acceptance

- At scales 1, 1.5, and 2, a positioned colored element yields the expected dimensions and pixel content.
- Fractional bounds use a consistent rounding policy without losing an edge.
- Scrolled pages, transformed elements, and iframe elements retain correct crop coordinates.
- Existing screenshot error propagation and PNG/JPEG support remain intact; browser fixtures are local and deterministic.
