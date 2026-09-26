# Public API tests

This directory holds tests that use only the exported API of the code they
cover. It mirrors the source layout:

- `tests/`: the root Rod package, with its shared browser harness in
  `setup_test.go` and the fake protocol clients that several test files use in
  `fake_client_test.go`. A fake that one topic needs stays in that topic's file.
- `tests/lib/cdp/`: browser-backed CDP transport tests.
- `tests/lib/launcher/`: browser launch tests and the manager benchmark.

Place a test of the root package here when the exported API can express it.
Browser-free tests install a fake `rod.CDPClient` with `rod.New().Client`, open
pages with `Browser.PageFromSession`, and deliver protocol events after
`Browser.Connect`. Most browser tests call `setup(t)`, which launches Chrome on
first use and shares it with later tests. A few tests start their own browser
instead, such as `TestConfiguredBrowserLaunch`, `TestBrowserPool`, and
`TestCrossProcessFrameSession`, so a test that does not call `setup(t)` is not
necessarily browser-free.

The repository root keeps only the root package tests that need unexported
identifiers, in `*_private_test.go` files grouped by subsystem with shared
helpers in `helpers_private_test.go`, and the Go documentation examples in
`examples_test.go`, which document the package. The other tests of `lib`
packages, command tests, and generated protocol tests remain beside their
source. The existing modules and dependency boundaries are unchanged.

From the repository root, run `bash scripts/check.sh pure` for checks that need
no browser, `bash scripts/check.sh browser` for the complete browser suite, or
`bash scripts/check.sh live` for executable Go documentation examples. Pure mode
selects browser-free tests by name: add a new one to `rod_public_tests` for this
directory or to `rod_tests` for the root package in `scripts/check.sh`.

To run one integration package with coverage of its production code:

```sh
GODEBUG=tracebackancestors=100 go test -count=1 -race -cover -covermode=atomic \
  -coverpkg=github.com/rah-0/rod -p=1 -parallel=1 ./tests
```

For the CDP or launcher suites, use `./tests/lib/cdp` or `./tests/lib/launcher`
and the matching `github.com/rah-0/rod/lib/...` import path for `-coverpkg`.
Fixtures remain under the source directories; tests resolve their paths without
changing the process working directory.
