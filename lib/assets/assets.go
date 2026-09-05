// Package assets contains Rod monitor pages and cursor artwork.
package assets

import _ "embed"

// MousePointer contains the cursor artwork used by Rod.
//
//go:embed mouse-pointer.svg
var MousePointer string

// Monitor contains the page-selection monitor.
//
//go:embed monitor.html
var Monitor string

// MonitorPage contains the monitor for an individual page.
//
//go:embed monitor-page.html
var MonitorPage string
