# Browser integration tests

This directory mirrors the packages tested through their public APIs:

- `tests/`: the root Rod package and its shared browser harness.
- `tests/lib/cdp/`: browser-backed CDP transport tests.
- `tests/lib/launcher/`: browser launch tests and the manager benchmark.

Unit tests, tests of private implementation details (including browser-dependent
cases), command tests, generated protocol tests, and Go documentation examples
remain beside their source.
The existing modules and dependency boundaries are unchanged.

From the repository root, run `bash scripts/check.sh pure` for checks that need
no browser, `bash scripts/check.sh browser` for the complete browser suite, or
`bash scripts/check.sh live` for executable Go documentation examples.

To run one integration package with coverage of its production code:

```sh
GODEBUG=tracebackancestors=100 go test -count=1 -race -cover -covermode=atomic \
  -coverpkg=github.com/rah-0/rod -p=1 -parallel=1 ./tests
```

For the CDP or launcher suites, use `./tests/lib/cdp` or `./tests/lib/launcher`
and the matching `github.com/rah-0/rod/lib/...` import path for `-coverpkg`.
Fixtures remain under the source directories; tests resolve their paths without
changing the process working directory.
