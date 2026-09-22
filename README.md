# rod

## Credits

Rod was created by [Yad Smood](https://github.com/ysmood) and developed with
the [go-rod contributors](https://github.com/go-rod/rod/graphs/contributors).
This repository is a maintained fork of
[go-rod/rod](https://github.com/go-rod/rod). The original and fork copyright
notices are preserved in the [MIT license](LICENSE).

## Overview

A high-level Go driver for the Chrome DevTools Protocol, built for browser automation and scraping.

- Chainable, context-aware API with automatic waiting
- Support for nested frames and shadow DOMs
- Page interaction, request interception, screenshots, PDFs, and downloads
- Concurrent-safe browser operations
- Dependency-free core module
- Direct local browser execution without downloaded binaries

## Requirements

- Go 1.27.1 or later
- Chrome, Chromium, or Edge installed locally

See the [platform support matrix](doc/PLATFORM_SUPPORT.md) for exact tested OS,
CPU, browser, and Go versions, plus known cleanup limitations on other systems.

Rod never downloads a browser or helper executable. Use `launcher.Launcher.Bin`
when an explicit browser path is required. Local launches use a fresh temporary
automation profile by default; using an installed browser does not require
using its personal profile. Set `Launcher.UserDataDir` explicitly when session
state should persist.

## Installation

```sh
go get github.com/rah-0/rod
```

See the [API reference](https://pkg.go.dev/github.com/rah-0/rod).
For versioned migration notes, see [Breaking changes](doc/BREAKING.md).

## Events

Subscribe before triggering an action, then call the returned wait function:

```go
wait := page.EachEvent(rod.On(func(event *proto.PageLoadEventFired, _ proto.TargetSessionID) bool {
    fmt.Println("page loaded at", event.Timestamp)
    return true
}))
page.MustNavigate("https://example.com")
if err := wait(); err != nil {
    panic(err)
}
```

Return `false` to keep receiving events. Pass multiple `rod.On` handlers to
observe different event types in message order. Browser handlers receive events
across sessions; page handlers only receive their page's events. For one event,
use `var event proto.PageLoadEventFired` and `wait := page.WaitEvent(&event)`.
Cancel the page or browser context to end a continuing subscription.

## Runnable examples

Runnable examples and their usage documentation live in [examples/](examples/).

| Example | Demonstrates |
| --- | --- |
| [Page diagnostics](examples/page-diagnostics/main.go) | Initial console messages, exceptions, and HTTP failures. |
| [Owned launch](examples/owned-launch/main.go) | Configured process ownership and cleanup after a deadline. |
| [JSON evaluation](examples/json-evaluation/main.go) | Typed DOM projection, discarded results, and serialization errors. |
| [Typing](examples/typing/main.go) | Validated keys, Enter actions, and Unicode insertion. |
| [HTTP fixture](examples/http-fixture/main.go) | Relative scripts and API responses with configuration before navigation. |
| [Query selection](examples/query-selection/README.md) | CSS, XPath, JavaScript, frame and shadow-root queries, and bounded waits. |
| [Forms](examples/forms/README.md) | Visibility waits, file uploads from memory, and form submission. |
| [Native drag and drop](examples/drag/README.md) | Explicit drag data, native drop events, and verified payload delivery. |
| [Capture](examples/capture/main.go) | Page and element screenshots, device emulation, and PDF output. |
| [Screencast](examples/screencast/README.md) | Record timestamped frames and pauses, with optional MP4 conversion. |
| [Network](examples/network/main.go) | Request headers and cookies configured before navigation. |
| [Browser response](examples/browser-response/README.md) | Capture an authenticated POST response without replaying the request. |
| [Download](examples/download/main.go) | Download completion and verification of the saved bytes. |
| [Proxy authentication](examples/proxy-auth/main.go) | An authenticated local proxy and verified forwarding. |
| [Connect to a browser](examples/connect-browser/main.go) | Attach to a browser whose process belongs to its caller. |
| [Persistent profile](examples/persistent-profile/README.md) | A caller-owned automation profile with separate login state and preserved data. |
| [Managed launch](examples/launch-managed/main.go) | An authenticated manager and remotely requested browser lifecycle. |
| [Load an extension](examples/load-extension/main.go) | A Manifest V3 extension changing a local page in headless Chrome. |
| [Tab metadata](examples/tab-metadata/README.md) | Read tab selection, order, pinning, and grouping without activating tabs. |
| [Custom WebSocket](examples/custom-websocket/main.go) | A third-party transport adapter with cancellation and cleanup. |
| [E2E test project](examples/e2e-testing/calculator_test.go) | Native Go tests with a shared process and isolated browser contexts. |

## Development

The core module has no third-party Go dependencies. Protocol serialization uses
standard `encoding/json`; direct `encoding/json/v2` imports are prohibited.
Protocol regeneration reads the installed Chrome, Chromium, or Edge browser.
Device generation uses the checked-in device profiles; JavaScript generation
uses a local Node.js executable:

```sh
go generate ./...
```

Generated Go files remain checked in, so ordinary builds do not launch a browser.
See [protocol generation](lib/proto/generate/README.md) for browser selection,
provenance, freshness checks, and explicit offline input, and
[device generation](lib/devices/README.md) for profile updates. Generators validate
their output before replacing owned files.

Check formatting, vet, compilation of all four modules with and without the
workspace, and race tests that need no browser or container:

```sh
bash scripts/check.sh pure
bash scripts/check.sh fix
```

The second command prints advisory `go fix -diff` suggestions for review; it
does not apply them. Review protocol encoding before applying suggestions, and
keep explicit `WaitGroup.Add`/`Done` calls around callbacks that may panic or call
`runtime.Goexit`.

Run unit tests, browser integration tests, and all example modules with an
installed browser:

```sh
bash scripts/check.sh browser
```

This also compares the installed browser's protocol with the generated bindings,
prints the browser version, and fails when regeneration is needed. Runtime tests
still run when the freshness check fails. Keep the browser installation updated
to expose protocol changes and compatibility regressions in newer releases.
Environments that require disabling sandboxing for protocol capture can use
`ROD_PROTOCOL_NO_SANDBOX=1 bash scripts/check.sh browser`.

The test runner runs packages and test cases sequentially (`-p=1 -parallel=1`).
Public API browser integration tests mirror the source layout in [tests/](tests/README.md).
Unit tests, command tests, generated tests, and Go documentation examples stay
beside their packages. The suite in `tests/` reuses one browser, retires it after
a failed test, and bounds graceful shutdown before killing owned processes and
removing profiles. Integration and e2e browser cases stay sequential even with a
higher `-parallel` setting. The cleanup benchmark also runs one browser lifecycle
at a time.

Executable documentation examples run separately with `bash scripts/check.sh live`.
Tests store screenshots, PDFs, and failed-test CDP logs in `t.ArtifactDir()`.
To retain these files from the Rod integration tests:

```sh
mkdir -p /tmp/rod-artifacts
GODEBUG=tracebackancestors=100 go test -count=1 -race -cover -covermode=atomic \
  -coverpkg=github.com/rah-0/rod -run '^Test' \
  -artifacts -outputdir /tmp/rod-artifacts ./tests
```

Successful-test CDP logs are removed. Without `-artifacts`, Go removes artifact
directories after the test.

The separate [Testcontainers compatibility test](lib/docker) uses
`WithAlwaysPull()` to test `docker.io/chromedp/headless-shell:latest` on every
run. The floating tag is intentional so newly published browser versions are
checked for compatibility:

```sh
go test -count=1 -race -cover -covermode=atomic ./lib/docker
```

Nested modules retain their local `replace` directives.

## License

Rod is available under the [MIT License](LICENSE).

## ☕ Support

If this saved you time or brought value to your project, feel free to show some support. Every bit is appreciated 🙂

[![Buy Me A Coffee](https://cdn.buymeacoffee.com/buttons/default-orange.png)](https://www.buymeacoffee.com/rah.0)
