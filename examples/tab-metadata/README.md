# Tab metadata

Run from the repository root:

```sh
go run ./examples/tab-metadata
```

The example starts a private headless browser, creates one blank tab, and prints
a JSON snapshot of its tab targets. The query itself only calls
`Target.getTargets`; it does not activate tabs, bring windows forward, or inject
page scripts. Use the same query with an already connected browser:

```go
result, err := (proto.TargetGetTargets{Filter: proto.TargetTargetFilter{
    {Type: "tab"},
    {Exclude: true},
}}).Call(browser)
```

Filters match in order: include tab targets, then exclude everything else.
The default filter excludes tabs, and `Browser.Pages` returns page targets.
Tab and page target IDs are different; neither URL matching nor enumeration
order establishes which pages belong to a tab.

`TargetTargetInfo.EmbedderData` preserves the browser's optional experimental
metadata as `map[string]jsonvalue.Value`, including unknown keys:

| Chrome key | Meaning when present |
| --- | --- |
| `tabStripIndex` | Zero-based position within the tab's window. Target enumeration order is not tab-strip order. |
| `tabActive` | Whether the tab is selected within its Chrome window. This does not identify the OS foreground window. |
| `tabPinned` | Whether the tab is pinned. |
| `tabGroupId` | Optional tab-group identifier. |

Check presence before interpreting a value:

```go
if active, available := info.EmbedderData["tabActive"]; available {
    fmt.Println("Selected within window:", active.Bool())
} else {
    fmt.Println("Tab selection metadata unavailable")
}
```

Older browsers and other embedders may omit metadata or individual fields.
An absent map remains nil; missing `tabActive` is unavailable, not an inactive
tab. The example prints the supplied metadata without filling in defaults.

This is a snapshot. Poll `Target.getTargets` when updates are needed;
metadata-only changes do not trigger `Target.targetInfoChanged` in the
[Chromium implementation that introduced these fields](https://chromium.googlesource.com/chromium/src/+/5aa804ae0b62bd1b0d54f57494211239e2ed5ffe).
See [protocol generation](../../lib/proto/generate/README.md) for installed-browser
generation and explicit saved-schema reproduction.
