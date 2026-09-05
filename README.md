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

## Differences from upstream Rod

This fork keeps Rod's CDP-based automation API while changing browser setup,
dependencies, and process ownership. The main differences from
[go-rod/rod](https://github.com/go-rod/rod) are:

| Area | This fork |
| --- | --- |
| Go and dependencies | Requires Go 1.27.1. The core module has no third-party Go dependencies; upstream's [helper dependencies](https://github.com/go-rod/rod/blob/main/go.mod) have been removed or replaced with repository-owned code. Testcontainers dependencies stay in a separate test module. |
| Browser provisioning | Uses an installed Chrome, Chromium, or Edge executable, or an explicit `Launcher.Bin` path. Upstream's [automatic browser downloads and revision selection](https://github.com/go-rod/rod/blob/main/lib/launcher/launcher.go) have been removed. A missing browser returns an error. |
| Process lifecycle | Launches the browser directly, without upstream's `leakless` helper executable. Call `Browser.Close` or `Launcher.Kill` during normal shutdown; abrupt process termination has no portable cleanup guarantee. `Launcher.Cleanup` preserves caller-supplied profiles. See [launcher lifecycle](lib/launcher). |
| JSON types | Replaces `gson.JSON` with [`lib/jsonvalue.Value`](lib/jsonvalue). Serialization uses standard `encoding/json`; direct `encoding/json/v2` imports are prohibited. |
| Remote management | Adds mandatory bearer authentication, loopback-only listening by default, and restrictions on client-supplied launch settings. Use TLS or an encrypted tunnel for remote access. The manager owns each session's browser and temporary profile. See [remote manager configuration](lib/launcher#remote-manager-security). |
| Development monitor | Listens on loopback by default and has no authentication. Use an authenticated proxy when exposing it remotely. |
| Browser compatibility testing | Adds an explicit [Testcontainers test](lib/docker) that pulls `chromedp/headless-shell:latest` on every run to detect compatibility changes. Container execution is optional; local development continues to use an installed browser. |

When migrating, switch imports to `github.com/rah-0/rod` and adapt any direct
`gson.JSON` usage to `lib/jsonvalue.Value`. Download/revision and `Leakless`
APIs are no longer available. Managed clients now pass a token to
`NewManaged(serviceURL, authToken)` or `MustNewManaged(serviceURL, authToken)`;
the server uses `NewManager(authToken)`. Remote `KeepUserDataDir` and the
manager's `--allow-all` option have also been removed.

Command-line defaults are loaded through `defaults.Load()` rather than package
initialization. Constructors call it automatically; applications that call
`flag.Parse()` or override exported defaults must call it first.

## Requirements

- Go 1.27.1 or later
- Chrome, Chromium, or Edge installed locally

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

## Development

The core module has no third-party Go dependencies. Code generation requires a
locally installed Chromium-family browser and Node.js executable; it downloads
neither.

Run the root test suite without live-site documentation examples:

```sh
GODEBUG=tracebackancestors=100 go test -count=1 -race -cover -covermode=atomic -run '^Test' ./...
```

The separate [Testcontainers compatibility test](lib/docker) uses
`WithAlwaysPull()` to test `docker.io/chromedp/headless-shell:latest` on every
run. The floating tag is intentional so newly published browser versions are
checked for compatibility:

```sh
go test -count=1 -race -cover -covermode=atomic ./lib/docker
```

## License

Rod is available under the [MIT License](LICENSE).

## ☕ Support

If this saved you time or brought value to your project, feel free to show some support. Every bit is appreciated 🙂

[![Buy Me A Coffee](https://cdn.buymeacoffee.com/buttons/default-orange.png)](https://www.buymeacoffee.com/rah.0)
