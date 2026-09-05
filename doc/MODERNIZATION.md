# Go modernization roadmap

Rod targets Go 1.27.1 and has a dependency-free core. This record tracks the modernization released in v0.118.0 on 2026-09-05, including resource handling, tests, and code generation.

The checklist covers the library, generators, tests, examples, and JavaScript helpers. Checked items are complete; optional design changes are marked separately.

## Priorities

- **P1:** Correctness, resource handling, and test reliability.
- **P2:** Recommended maintenance improvements.
- **P3:** Small cleanup or an optional design change. Optional items are explicitly identified.

Checked items are implemented or, for T08, evaluated with the design decision recorded. Unchecked items are explicitly optional and deferred. Generator sources and outputs must continue to change together. Breaking Go API changes are welcome when they simplify the code; do not retain wrappers or duplicate APIs solely for backward compatibility.

## Language and standard-library idioms

- [x] **M01 · P2 · Replace `interface{}` with `any` throughout.**
  Replaced empty interfaces with `any` in library APIs, templates, mocks, tests, and examples. Concrete interfaces and `jsonvalue.Value` retain their behavior.

- [x] **M02 · P2 · Use `reflect.TypeFor[T]()` when the type is known statically.**
  The protocol template and regenerated type registry use `reflect.TypeFor[T]()`. Runtime type discovery still uses `TypeOf`.

- [x] **M03 · P3 · Finish modernizing reflection names and iteration.**
  Replaced `reflect.Ptr` with `reflect.Pointer`. T07 removed the reflective suite runner, so its field/method loops no longer need modernization.

- [x] **M04 · P2 · Replace pointer-construction helpers with `new(expr)`.**
  Call sites use `new(expr)` with explicit element types where needed, including `float64` conversions. The redundant exported `jsonvalue.Num`, `Int`, `Str`, and `Bool` helpers are removed.

- [x] **M05 · P3 · Simplify eligible loops and remove old capture workarounds.**
  Eligible stable-bound loops use integer range, obsolete loop-variable captures are gone, and LCS reverse traversal uses `slices.Backward`. Stride-two and changing-bound loops remain.

- [x] **M06 · P2 · Replace small collection algorithms with `slices` functions.**
  Membership, sorting, and LCS insertion-point searches use `slices` functions. Pointer sharing and nil/empty equality contracts remain unchanged.

- [x] **M07 · P3 · Use `strings.Cut`, `CutPrefix`, and `CutLast` for two-part parsing.**
  Defaults parsing uses `strings.Cut`; stack filtering uses `CutPrefix`; monitor route suffixes use `CutLast`. The protocol method parser retains its existing malformed-input behavior.

- [x] **M08 · P3 · Use split iterators when the intermediate slice is unused.**
  Device-name parsing and goroutine ancestry-setting parsing use `strings.SplitSeq` where the intermediate slice was unused. Stack parsing retains slices where it indexes the lines.

- [x] **M09 · P3 · Use `strings.ReplaceAll` for all-occurrence replacement.**
  Protocol identifier normalization uses `strings.ReplaceAll` for all-occurrence replacement.

- [x] **M10 · P2 · Migrate non-cryptographic randomness to `math/rand/v2`.**
  Backoff jitter and example randomness use `math/rand/v2`. The manager benchmark no longer includes random sleeps. Retry tests inject a deterministic backoff; cryptographic randomness remains unchanged.

- [x] **M11 · P2 · Use typed atomics.**
  CDP request IDs, the launch guard, and test counters use typed atomics. The obsolete log-file counter was removed; used atomics are not copied.

- [x] **M12 · P2 · Use `sync.WaitGroup.Go` for owned goroutine tasks.**
  Eligible owned goroutines use `WaitGroup.Go`, including the rename utility, whose parsing failures now log and skip invalid locations. `utils.All` and Must-based examples keep explicit `Add`/`Done` because their callbacks may panic or call `Goexit`.

- [x] **M13 · P2 · Remove obsolete timer stop-and-drain sequences.**
  IdleCounter resets its timer directly; the leak checker stops abandoned timers without draining. Fake-time tests cover idle restart, cancellation, and reuse without stale ticks.

- [x] **M14 · P3 · Use `errors.AsType[T]` for typed error extraction.**
  Typed error extraction uses `errors.AsType[T]`; sentinel assertions use `errors.Is`. Exact-text assertions remain where text is the contract.

- [x] **M15 · P2 · Build generated source without repeated string concatenation.**
  Protocol, JavaScript, and device generators build accumulated source with builders and format the complete result before writing.

## Runtime behavior and resource ownership

These tasks address cancellation, resource cleanup, and error handling.

- [x] **R01 · P1 · Replace the pre-Go-1.15 TLS adapter.**
  The default TLS dialer uses `tls.Dialer.DialContext`. Regression coverage includes cancellation before dialing and during a stalled TLS handshake.

- [x] **R02 · P1 · Make the raw WebSocket handshake cancelable and close failed connections.**
  `WebSocket.Connect` gives its context ownership of establishment only. Deadlines and a synchronized cancellation callback interrupt raw handshake I/O; failed connections close, and successful connections clear establishment deadlines. Stalled reads/writes, rejection, cancellation, and post-connect use are covered.

- [x] **R03 · P1 · Bound HTTP discovery and generation requests.**
  `ResolveURL(ctx, u)` and `MustResolveURL(ctx, u)` require a context, with a 10-second HTTP bound, status checks, and response-body closure. Launch passes its context; there is no separate contextless wrapper. Protocol/device generation uses local snapshots, eliminating unbounded discovery/download requests; device checksum verification is preserved. Snapshot update procedures use explicitly bounded downloads.

- [x] **R04 · P1 · Fix output-file ownership and preserve I/O errors.**
  `utils.OutputFile` returns directory and open failures, closes files it opens for readers, and joins copy/close errors. Tests cover invalid targets and a reader failing after partial output. Existing JSON-encoding panic behavior is preserved.

- [x] **R05 · P1 · Return ordinary operational failures through existing error APIs.**
  Discovery and CDP request marshaling return operational errors. Invalid inbound CDP JSON terminates consumption and fails pending and subsequent calls with the terminal error. Browser discovery failure stops a newly launched browser, and `Open` checks process start before using `Process`. Migration requirements are recorded in [BREAKING.md](BREAKING.md).

- [ ] **R06 · P3 · Optionally replace cancellation-only watcher goroutines with `context.AfterFunc`.**
  Deferred optional implementation change. Existing cancellation watchers remain; replacing them requires synchronizing callbacks already running and does not simplify the current ownership boundaries enough to justify the change.

- [x] **R07 · P2 · Review ineffective JSON omission tags as a separate wire-contract task.**
  Presence tests lock in omitted optional pointers versus explicit null, false, zero, empty arrays/maps, and undefined JavaScript arguments. Generator policy removes only ineffective `omitempty` from `jsonvalue.Value` fields. Standard `encoding/json` wire behavior is preserved.

## Code generation and assets

- [x] **G01 · P2 · Emit standard generated-file and deprecation markers.**
  All three generators emit standard `Code generated ... DO NOT EDIT.` headers separately from package documentation. Protocol deprecation comments have a separate `Deprecated:` paragraph and describe the pinned browser schema.

- [x] **G02 · P1 · Make generation failure-safe and limit deletion to owned outputs.**
  Protocol generation recognizes its own current/historical headers, validates and formats every output, stages replacements, and deletes only obsolete owned files. Rollback restores originals; failed restoration retains backups and reports their path. Reserved filenames and handwritten outputs are protected. Other generators format before atomic file replacement. Failure and ownership tests cover these boundaries.

- [x] **G03 · P2 · Make protocol and device regeneration reproducible.**
  Protocol generation defaults to a checked-in Chrome 128.0.6568.0 schema with version/revision/checksum provenance. Device generation defaults to a checksummed snapshot from an immutable source pin. Package documentation defines explicit source, checksum, parser, and user-agent updates; generated values are determined by those pins rather than the installed browser.

- [x] **G04 · P3 · Replace plain asset-string generation with `go:embed`.**
  `MousePointer`, `Monitor`, and `MonitorPage` are embedded string variables with the same asset bytes. The SVG lives in `lib/assets`; the asset generator is removed. Asset sources use LF line endings for reproducible embedding.

- [x] **G05 · P1 · Generate assertions that verify protocol behavior.**
  Generated native tests assert exact request/event names and command routing, including result decoding. Small schema fixtures exercise optional fields, patches, deprecation, reserved names, deterministic formatting, and failure preservation.

## Tests and benchmarks

- [x] **T01 · P1 · Make benchmarks measure a defined operation and release resources promptly.**
  The manager benchmark uses `b.Loop` for one complete managed lifecycle and waits for server-side cleanup. The parallel cleanup benchmark keeps `RunParallel` and releases each owned browser/profile within its iteration. Both measure cleanup and exclude artificial random delays.

- [x] **T02 · P2 · Base test work on native test contexts.**
  Explicit test-helper and HTTP request contexts derive from native test contexts. E2e browser work uses native per-test contexts and separate bounded cleanup contexts. Root pooled Browser/Page work retains its suite context and separate owner so it remains reusable across tests.

- [x] **T03 · P2 · Use fake time for isolated Go concurrency tests.**
  Idle-counter, retry/cancellation, and delayed-callback tests use `testing/synctest`. Fake time replaces tight wall-clock windows; synchronization makes race assertions explicit. Browser/process timing remains real.

- [x] **T04 · P3 · Use Go 1.27's in-memory HTTP test server where appropriate.**
  The test router offers `ServeInMemory` and an explicit client for Go-only fixtures. `Serve` retains real loopback listeners for browser and default-client consumers.

- [x] **T05 · P2 · Distinguish disposable test files from retained artifacts.**
  Disposable output tests use `TempDir`; CDP logs, screenshots, and PDFs use `ArtifactDir`. Successful-test CDP logs are removed, and failed-test logs can be retained with `-artifacts`. Default filename behavior is tested in an isolated working directory.

- [x] **T06 · P1 · Give the e2e example's browsers explicit lifetimes.**
  The e2e suite connects lazily, closes its suite browser, and closes each test's incognito browser context. Selecting no tests no longer launches a browser; cleanup uses bounded contexts after native test cancellation.

- [x] **T07 · P2 · Replace the remaining reflective test suite with native subtests.**
  Handwritten and generated protocol receiver methods are native tests. The generator emits the new tests, and the reflective runner plus its dedicated harness tests have been removed. Assertion and fixture helpers remain.

- [x] **T08 · P3 · Evaluate native goroutine-leak diagnostics alongside the existing checks.**
  Evaluated the native `goroutineleak` profile against the current checks. It does not replace timeout-based lifecycle detection or ancestry attribution, so the existing diagnostics remain with an explanatory comment.

- [x] **T09 · P3 · Add focused native fuzz tests for pure parsing boundaries.**
  Bounded native fuzz targets cover stack parsing, JSON decoding/paths, and WebSocket frames without launching browsers, network servers, or containers.

## JavaScript and development tooling

- [x] **D01 · P3 · Finish the remaining JS declaration cleanup.**
  The five remaining JavaScript `var` declarations use `const` or `let`, and `helper.go` is regenerated. XMLHttpRequest behavior is unchanged.

- [x] **D02 · P2 · Keep monitor polling alive after a failed request.**
  Monitor polling checks response status, handles fetch/JSON/render failures, and schedules the next attempt in `finally`. DOM construction remains safe; embedded asset sources and behavior coverage are updated together.

- [x] **D03 · P2 · Establish verification for every module and generation boundary.**
  Local verification covers formatting, vet, all four modules with workspace and standalone resolution, advisory `go fix -diff`, race tests, and deterministic offline generation. Browser tests, live examples, and Docker/image-pull tests have separate script entry points. Local replacements remain intact.

- [x] **D04 · P3 · Reconcile stale comments and suppressions with the new tooling.**
  Generation documentation now describes Node function serialization and offline snapshots. Obsolete lint/spelling directives are removed, and the root generation workflow includes devices.

## API and implementation cleanup

- [x] **O01 · P3 · Replace reflective event callbacks with typed handlers.**
  `EachEvent` accepts `EventHandler` values constructed with `On`. Every callback receives a concrete event pointer and session ID and returns a stop flag. Mixed event types retain ordered dispatch. Generic `WaitEvent` and `Message.Load` require output pointers, eliminating runtime signature inspection and reflection-based callback invocation. Page destruction now decodes its target ID before deciding whether to cancel the page. Tests cover ordering, session filtering, domain restoration, cancellation, and shared decoding.

- [x] **O02 · P3 · Use typed pending requests and geometry comparison.**
  CDP tracks pending requests in a mutex-protected `map[int]chan result`, removing type assertions and per-request `sync.Once`. Responses claim their request under the lock and deliver outside it; tests cover canceled, duplicate, unknown, and out-of-order responses and a response immediately before EOF. Element stability compares typed coordinate slices without reflection. Nil and empty coordinate slices are equal; an uninitialized shape result remains distinct from an initialized empty result so animation-frame waiting still observes two frames. NaN coordinates remain unequal. Browser command state remains heterogeneous by design, and clones continue sharing their owners' locks.

## Project rules and design constraints

- Always use the standard `encoding/json` package with its default behavior. Direct adoption of `encoding/json/v2` is prohibited and is not a future migration option. Go 1.27's newer implementation behind `encoding/json` is an internal implementation detail; it does not change this project rule. JSON-related modernization, including R07, must stay within `encoding/json`. [Go 1.27 JSON changes](https://go.dev/doc/go1.27#encoding-json-v2).
- Preserve optional protocol pointers and the distinction between omitted fields and explicit zero/false/empty values. In particular, [lib/proto/generate/patch.go](../lib/proto/generate/patch.go) deliberately handles empty versus absent response bodies.
- Keep SHA-1 in [lib/cdp/websocket.go](../lib/cdp/websocket.go): it belongs to the WebSocket handshake protocol, so changing the hash would break interoperability.
- Do not blindly change [lib/launcher/launcher.go](../lib/launcher/launcher.go) to `exec.CommandContext(l.ctx, ...)`. `Launch` defers cancellation of that context at [Launch](../lib/launcher/launcher.go), so doing so would kill the browser when launch returns successfully. Any `Cmd.WaitDelay` change also needs an explicit process/pipe-drain policy.
- Preserve deliberately shared pointer mutexes/maps in Browser/Page clones; [context.go](../context.go) copies their outer structs. Converting all locks to value fields would change sharing or copy used locks.
- Do not replace `utils.RandString(n)` with `crypto/rand.Text()` without auditing consumers: the former returns `2*n` hexadecimal characters; the latter has a different length/alphabet contract. `crypto/rand.Text` is already used in manager code.
- Review floating-point behavior before using `min`/`max` in [lib/proto/a_patch.go](../lib/proto/a_patch.go): NaN and signed-zero handling can change geometry calculations. The typed stability comparator's equality rules are documented in O02.
- `log.Logger`, `sync.Once`, `sync.Map`, channels, `&T{}`, and small private helpers are not inherently obsolete. Adopt `slog`, iterators, generic abstractions, or different synchronization only for a demonstrated benefit.
- `lib/js/helper.js` deliberately uses `XMLHttpRequest` with its own response/error behavior; replacing it with `fetch` is not a syntax-only change. Keep current safe DOM construction in the monitor.
- The manual unset-environment test in [lib/defaults/defaults_test.go](../lib/defaults/defaults_test.go) must distinguish an absent key from an empty value; `t.Setenv(key, "")` is not equivalent.

The code already uses generic `Pool[T]` and observables, typed atomics, `bytes.Clone`, `os.ReadDir`, `io.ReadAll`, native test cleanup/temp/environment helpers, `os.Root`, and `//go:build` constraints. These need no migration. There are no `io/ioutil` imports, old `// +build` directives, or `rand.Seed` calls to replace.

## Migration history

[BREAKING.md](BREAKING.md) records incompatible APIs, behavior, and tooling by
version, newest first. Add changes under the planned next release version while
developing, retaining the previous-version comparison. At release time, replace
the planned status with the release date. Keep detailed migration instructions
there.

## Validation

Run from the repository root with Go 1.27.1 and Node.js installed:

```sh
bash scripts/check.sh pure
bash scripts/check.sh fix
go generate ./...
bash scripts/check.sh browser
```

`pure` checks formatting, vet, compilation, and Go-only race tests, including typed event dispatch and pending-request routing. It covers all four modules explicitly and checks workspace versus `GOWORK=off` resolution. The root `./...` package pattern alone excludes the three nested modules. `fix` prints suggestions for review without applying them.

`browser` requires a locally installed Chromium-family browser and sets the ancestry diagnostics environment. Live-site examples use `bash scripts/check.sh live`; Docker compatibility uses `bash scripts/check.sh docker` and intentionally pulls the latest browser image. Run these two checks explicitly when needed.

Generators run offline. After regenerating, inspect the diff and check for unexpected generated files. See [protocol generation](../lib/proto/README.md), [device generation](../lib/devices/README.md), and the [development workflow](../README.md#development).

### Completed validation

Validated locally on Go 1.27.1, Linux, with Node.js, Chrome, and Docker:

- `bash scripts/check.sh pure` passed: formatting, vet, compilation for all four modules with both workspace and standalone resolution, and Go-only race tests.
- All four modules passed standalone `go mod verify` and `go mod tidy -diff`; manifests and checksum files remained unchanged.
- `bash scripts/check.sh browser` passed: complete root and e2e suites with race/coverage. Root coverage was 99.9%, protocol was 100%, CDP was 97.8%, and launcher was 96.8%.
- `go test -mod=readonly -count=1 -race -cover -covermode=atomic ./lib/docker/...` passed after pulling the latest browser image.
- Regeneration left all 56 generated Go files byte-identical; their formatting is stable under another `gofmt` pass. Embedded assets no longer require generated Go output.
- All 800 former protocol suite cases remain: 788 generated and 12 handwritten native tests, plus new behavior tests. Presence tests also passed against the previous generated tags in an isolated baseline checkout.
- Focused fuzz runs passed for JSON values, JSON paths, stack parsing, and WebSocket frames. Both lifecycle benchmarks executed successfully with cleanup included in each operation; these smoke checks are not performance comparisons.
- `go fix -diff` was reviewed for every module. Its two remaining suggestions replace explicit wait groups in Must-based examples; they are intentionally retained because those callbacks may panic. The verification script accepts diff-only results and rejects analyzer failures.

Live-site examples were not run.
