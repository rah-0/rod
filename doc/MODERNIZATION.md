# Go modernization roadmap

Rod targets Go 1.27.1 and has a dependency-free core. This roadmap tracks opportunities to simplify older code, improve resource handling, and strengthen tests and code generation.

The checklist covers the library, generators, tests, examples, and JavaScript helpers. Checked items are complete; optional design changes are marked separately.

## Priorities

- **P1:** Correctness, resource handling, and test reliability.
- **P2:** Recommended maintenance improvements.
- **P3:** Small cleanup or an optional design change. Optional items are explicitly identified.

Source links identify the relevant implementations. Update generators together with their outputs, and preserve public API and protocol behavior unless a task explicitly calls for a compatibility change.

## Language and standard-library idioms

- [ ] **M01 · P2 · Replace `interface{}` with `any` throughout.**
  There are 113 occurrences across 31 Go files. Since `any` is an alias for `interface{}`, this replacement preserves type compatibility and JSON behavior.
  Examples: [lib/cdp/client.go:19](../lib/cdp/client.go#L19), [page_eval.go:39](../page_eval.go#L39), [must.go:28](../must.go#L28), [lib/utils/utils.go:44](../lib/utils/utils.go#L44), and [lib/proto/generate/patch.go:11](../lib/proto/generate/patch.go#L11). Include `[]interface{}`, `map[string]interface{}`, variadic parameters, callbacks, mocks, and documentation examples. Keep concrete interfaces such as `io.Reader`; keep `jsonvalue.Value` where it supplies actual behavior. [Go specification: predeclared identifiers](https://go.dev/ref/spec#Predeclared_identifiers).

- [ ] **M02 · P2 · Use `reflect.TypeFor[T]()` when the type is known statically.**
  [lib/proto/definitions.go:13](../lib/proto/definitions.go#L13) contains **1,339** registry entries using `reflect.TypeOf(T{})`. Update the template at [lib/proto/generate/main.go:62](../lib/proto/generate/main.go#L62), then regenerate; also update [lib/proto/a_interface_test.go:52](../lib/proto/a_interface_test.go#L52). Retain `TypeOf(value)` when discovering an actual runtime value's type. [reflect.TypeFor](https://pkg.go.dev/reflect#TypeFor).

- [ ] **M03 · P3 · Finish modernizing reflection names and iteration.**
  Replace the old `reflect.Ptr` alias with `reflect.Pointer` at [browser.go:373](../browser.go#L373) and [utils.go:48](../utils.go#L48). If the reflective test runner is retained after T07, its `NumMethod`/`Method` and `NumField`/`Field` loops at [internal/testutil/each.go:134](../internal/testutil/each.go#L134) can use `Type.Methods()` and `Type.Fields()`. [reflect.Type](https://pkg.go.dev/reflect#Type).

- [ ] **M04 · P2 · Replace pointer-construction helpers with `new(expr)`.**
  The four helpers at [lib/jsonvalue/value.go:330](../lib/jsonvalue/value.go#L330) have 21 `jsonvalue.Num/Int/Str/Bool` call occurrences in the tree. Examples: [lib/devices/device.go:82](../lib/devices/device.go#L82), [lib/input/keyboard.go:107](../lib/input/keyboard.go#L107), and [page_test.go:854](../page_test.go#L854). Use `new(5)`, `new(info.Location)`, or `new(float64(1))` as appropriate; `new(1)` produces `*int`, so preserve the helpers' parameter conversions. A temporary used only to take its address at [lib/proto/a_patch.go:111](../lib/proto/a_patch.go#L111) can become `new(q.Center())`. Removing the exported helper functions is a separate compatibility decision. [Go 1.26 language changes](https://go.dev/doc/go1.26#language).

- [ ] **M05 · P3 · Simplify eligible loops and remove old capture workarounds.**
  Use integer range for stable bounds at [utils.go:99](../utils.go#L99), [utils.go:121](../utils.go#L121), [input.go:305](../input.go#L305), and [lib/proto/a_patch.go:99](../lib/proto/a_patch.go#L99). Remove redundant `method := method` at [internal/testutil/each.go:34](../internal/testutil/each.go#L34) and `i := i` at [lib/cdp/client_test.go:332](../lib/cdp/client_test.go#L332). The reverse slice traversal at [lcs.go:13](../lcs.go#L13) can use `slices.Backward`. Keep stride-two loops, loops with changing bounds, and counters used after the loop. [Loop variable semantics](https://go.dev/blog/loopvar-preview), [Go specification: range](https://go.dev/ref/spec#For_range).

- [ ] **M06 · P2 · Replace small collection algorithms with `slices` functions.**
  Replace the private membership helper at [lib/devices/utils.go:6](../lib/devices/utils.go#L6) and membership loops at [page.go:749](../page.go#L749) and [internal/goroutines/trace.go:34](../internal/goroutines/trace.go#L34) with `slices.Contains`. Use `slices.Sort` at [lib/launcher/launcher.go:383](../lib/launcher/launcher.go#L383), `slices.SortFunc` with an explicit comparator at [page_test.go:58](../page_test.go#L58), and `slices.BinarySearch` at [lcs.go:23](../lcs.go#L23), retaining its insertion index. Existing `sort` functions remain valid; the goal is simpler code. [slices package](https://pkg.go.dev/slices).

- [ ] **M07 · P3 · Use `strings.Cut`, `CutPrefix`, and `CutLast` for two-part parsing.**
  `name=value` parsing at [lib/defaults/defaults.go:186](../lib/defaults/defaults.go#L186) can use `Cut`. Prefix checking/removal at [internal/goroutines/trace.go:232](../internal/goroutines/trace.go#L232) can use `CutPrefix`. Last-path-component extraction at [dev_helpers.go:79](../dev_helpers.go#L79) and [dev_helpers.go:86](../dev_helpers.go#L86) can use `CutLast`. Preserve missing-separator behavior. The method parser at [lib/proto/a_interface.go:46](../lib/proto/a_interface.go#L46) currently panics without a dot and discards later components with extra dots; changing that parser needs an explicit malformed-input contract. [strings package](https://pkg.go.dev/strings).

- [ ] **M08 · P3 · Use split iterators when the intermediate slice is unused.**
  [lib/devices/generate/main.go:95](../lib/devices/generate/main.go#L95) and [internal/goroutines/trace.go:305](../internal/goroutines/trace.go#L305) can range over `strings.SplitSeq`. Keep `strings.Split` where code indexes, counts, retains, or revisits the parts. [strings.SplitSeq](https://pkg.go.dev/strings#SplitSeq).

- [ ] **M09 · P3 · Use `strings.ReplaceAll` for all-occurrence replacement.**
  Replace `strings.Replace(n, "Ids", "IDs", -1)` at [lib/proto/generate/utils.go:161](../lib/proto/generate/utils.go#L161). Keep calls with a deliberate limit such as `1`. [strings.ReplaceAll](https://pkg.go.dev/strings#ReplaceAll).

- [ ] **M10 · P2 · Migrate non-cryptographic randomness to `math/rand/v2`.**
  Three files import the old package: [lib/utils/sleeper.go:6](../lib/utils/sleeper.go#L6), [lib/launcher/load_test.go:5](../lib/launcher/load_test.go#L5), and [examples_test.go:7](../examples_test.go#L7). Preserve jitter bounds and inject a seeded local generator where repeatable tests matter. Random sequences will change. Keep tokens and security-sensitive names on `crypto/rand`. [math/rand/v2](https://pkg.go.dev/math/rand/v2).

- [ ] **M11 · P2 · Use typed atomics.**
  Replace `uint64` plus `atomic.AddUint64` at [lib/cdp/client.go:47](../lib/cdp/client.go#L47) with `atomic.Uint64`. The launch guard at [lib/launcher/launcher.go:45](../lib/launcher/launcher.go#L45) can use `atomic.Bool` and `CompareAndSwap(false, true)`. Update primitive test counters at [setup_test.go:295](../setup_test.go#L295) and [setup_test.go:386](../setup_test.go#L386) similarly. Audit copies: typed atomics must not be copied after first use. [sync/atomic](https://pkg.go.dev/sync/atomic).

- [ ] **M12 · P2 · Use `sync.WaitGroup.Go` for owned goroutine tasks.**
  Candidates include [lib/utils/utils.go:157](../lib/utils/utils.go#L157), [lib/utils/rename/main.go:27](../lib/utils/rename/main.go#L27), and [examples_test.go:665](../examples_test.go#L665). Remove the matching `Add`/`Done` plumbing. Keep wait groups that track external callbacks rather than newly launched goroutines. `WaitGroup.Go` requires its function not to panic; account for Rod's `Must*`/panic conventions before migrating public helpers or examples. [sync.WaitGroup.Go](https://pkg.go.dev/sync#WaitGroup.Go).

- [ ] **M13 · P2 · Remove obsolete timer stop-and-drain sequences.**
  [lib/utils/utils.go:232](../lib/utils/utils.go#L232) can reset its channel timer without the old drain dance; [internal/goroutines/leak.go:36](../internal/goroutines/leak.go#L36) can simply stop the abandoned timer. Preserve locks and stops that deliberately suspend activity. Check reset, idle restart, and cancellation boundaries. Go 1.27 permanently removes the old asynchronous timer-channel mode. [time.Timer.Reset](https://pkg.go.dev/time#Timer.Reset), [Go 1.27 runtime changes](https://go.dev/doc/go1.27#runtime).

- [ ] **M14 · P3 · Use `errors.AsType[T]` for typed error extraction.**
  Examples: [examples_test.go:216](../examples_test.go#L216), [setup_test.go:276](../setup_test.go#L276), and [element_test.go:126](../element_test.go#L126). This can replace the separate target variable and `errors.As(err, &target)` call. Prefer `errors.Is` for sentinel matching; assertions inside an error's own `Is` method are intentional matching logic. Exact error-text assertions should remain only where text itself is the contract. [errors.AsType](https://pkg.go.dev/errors#AsType).

- [ ] **M15 · P2 · Build generated source without repeated string concatenation.**
  [lib/js/generate/main.go:20](../lib/js/generate/main.go#L20) and [lib/proto/generate/main.go:61](../lib/proto/generate/main.go#L61) repeatedly append to growing strings. Use `strings.Builder` or write the template directly to a buffer, then format the completed source. Keep this scoped to repeated accumulation; ordinary short concatenations are fine. [strings.Builder](https://pkg.go.dev/strings#Builder).

## Runtime behavior and resource ownership

These tasks address cancellation, resource cleanup, and error handling.

- [x] **R01 · P1 · Replace the pre-Go-1.15 TLS adapter.**
  Completed 2026-09-05: [lib/cdp/websocket.go:66](../lib/cdp/websocket.go#L66) now selects `&tls.Dialer{}`; the context-discarding adapter and its obsolete TODO were removed from [lib/cdp/utils.go](../lib/cdp/utils.go). Custom dialers and the default TLS port retain their existing behavior. [TestWebSocketTLSCancellation](../lib/cdp/websocket_private_test.go#L52) covers cancellation before dialing and during a stalled TLS handshake using a local TCP fixture. The stalled-handshake test reproduced the failure before the fix. The separate raw WebSocket upgrade lifecycle in R02 remains outstanding. [tls.Dialer.DialContext](https://pkg.go.dev/crypto/tls#Dialer.DialContext).

- [ ] **R02 · P1 · Make the raw WebSocket handshake cancelable and close failed connections.**
  [lib/cdp/websocket.go:49](../lib/cdp/websocket.go#L49) stores the connection and returns the handshake error without closing it. The handshake at [lib/cdp/websocket.go:196](../lib/cdp/websocket.go#L196) uses raw connection I/O: attaching a context to an `http.Request` does not make `Request.Write` or `http.ReadResponse` observe cancellation. Apply connection deadlines/cancellation and close on failure. Define whether the supplied context owns only establishment or the entire connection; clear establishment deadlines after success. Test stalled, rejected, and canceled handshakes.

- [ ] **R03 · P1 · Bound HTTP discovery and generation requests.**
  [lib/launcher/url_parser.go:122](../lib/launcher/url_parser.go#L122), [lib/proto/generate/utils.go:29](../lib/proto/generate/utils.go#L29), and [lib/devices/generate/main.go:78](../lib/devices/generate/main.go#L78) use the default `http.Get` without a timeout. Introduce a context-aware discovery path and explicit HTTP client policy; check status before decoding. Preserve response-body closing and device checksum verification. For exported `ResolveURL`, adding/replacing a context parameter is an API decision. [http.NewRequestWithContext](https://pkg.go.dev/net/http#NewRequestWithContext).

- [ ] **R04 · P1 · Fix output-file ownership and preserve I/O errors.**
  [lib/utils/utils.go:128](../lib/utils/utils.go#L128) ignores directory-creation errors; its `io.Reader` branch ignores file-open errors and never closes the opened file. Return errors from setup, copying, and closing, using `errors.Join` when both copy and close fail. Add behavior tests for an invalid output target and a failing reader. Keep this independent from the mechanical `any` conversion. [errors.Join](https://pkg.go.dev/errors#Join).

- [ ] **R05 · P1 · Return ordinary operational failures through existing error APIs.**
  `ResolveURL` calls panic helper `utils.E` on read/parse failures at [lib/launcher/url_parser.go:128](../lib/launcher/url_parser.go#L128). `Client.Call` marshals parameters through a panic helper at [lib/cdp/client.go:99](../lib/cdp/client.go#L99), while its background reader panics on invalid protocol JSON at [lib/cdp/client.go:149](../lib/cdp/client.go#L149). Return marshaling/discovery errors and terminate/fail pending calls explicitly on malformed inbound data. Also check `cmd.Start` before accessing `cmd.Process` in [lib/launcher/browser.go:68](../lib/launcher/browser.go#L68). Preserve intentionally panicking `Must*` entry points and identify any changed panic contracts in release notes.

- [ ] **R06 · P3 · Optionally replace cancellation-only watcher goroutines with `context.AfterFunc`.**
  Candidates: subscription cleanup at [internal/observable/observable.go:42](../internal/observable/observable.go#L42) and monitor shutdown at [dev_helpers.go:53](../dev_helpers.go#L53). Keep event-delivery goroutines. Stop registrations when their resource ends and handle a callback already running; the returned stop function does not wait for completion. Validate cancellation races and cleanup behavior. [context.AfterFunc](https://pkg.go.dev/context#AfterFunc).

- [ ] **R07 · P2 · Review ineffective JSON omission tags as a separate wire-contract task.**
  The modernization analyzer flags `jsonvalue.Value` struct fields at [lib/proto/accessibility.go:189](../lib/proto/accessibility.go#L189) and [lib/proto/runtime.go:135](../lib/proto/runtime.go#L135), [258](../lib/proto/runtime.go#L258), and [592](../lib/proto/runtime.go#L592). `omitempty` does not omit these struct fields. Removing the ineffective option preserves current encoding; using `omitzero`, an `IsZero` method, or pointers could change it. Specify and test omitted versus explicit `null`, false, zero, empty collections, and undefined JS argument behavior first. Fix generator policy at [lib/proto/generate/utils.go:109](../lib/proto/generate/utils.go#L109), not just output tags. [encoding/json](https://pkg.go.dev/encoding/json#Marshal).

## Code generation and assets

- [ ] **G01 · P2 · Emit standard generated-file and deprecation markers.**
  The four generator headers at [lib/proto/generate/main.go:15](../lib/proto/generate/main.go#L15), [lib/js/generate/main.go:15](../lib/js/generate/main.go#L15), [lib/assets/generate/main.go:13](../lib/assets/generate/main.go#L13), and [lib/devices/generate/main.go:60](../lib/devices/generate/main.go#L60) should emit `// Code generated ... DO NOT EDIT.` separately from package documentation. Protocol deprecation formatting at [lib/proto/generate/main.go:104](../lib/proto/generate/main.go#L104) should emit a separate `Deprecated:` paragraph. Preserve deprecated protocol APIs unless a separate removal is intended. [Generated-code convention](https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source), [Go deprecation comments](https://go.dev/doc/comment#deprecation).

- [ ] **G02 · P1 · Make generation failure-safe and limit deletion to owned outputs.**
  [lib/proto/generate/main.go:241](../lib/proto/generate/main.go#L241) deletes every top-level `.go` file whose name does not start with `a_`, before regeneration. This can erase newly added handwritten code and leave incomplete output after a failure. Track generated ownership explicitly, render and validate all output before replacement, and remove only obsolete owned files. Use `go/format.Source` before writing instead of formatting already-overwritten files through subprocesses at [lib/proto/generate/main.go:85](../lib/proto/generate/main.go#L85), [lib/js/generate/main.go:35](../lib/js/generate/main.go#L35), and [lib/devices/generate/main.go:69](../lib/devices/generate/main.go#L69). Test generator failure without damaging existing files. [go/format.Source](https://pkg.go.dev/go/format#Source).

- [ ] **G03 · P2 · Make protocol and device regeneration reproducible.**
  [lib/proto/generate/utils.go:16](../lib/proto/generate/utils.go#L16) takes its schema from whichever browser is installed. Support an explicit schema fixture/snapshot and record browser/schema provenance. The device source is pinned and checksummed at [lib/devices/generate/main.go:16](../lib/devices/generate/main.go#L16), but the UA fallback/substitution remains Chrome `114.0.0.0` at [lib/devices/generate/main.go:106](../lib/devices/generate/main.go#L106). Define a deliberate source/checksum/parser/UA update procedure. Keep immutable pins; substituting an unversioned latest URL would reduce reproducibility.

- [ ] **G04 · P3 · Optionally replace plain asset-string generation with `go:embed`.**
  [lib/assets/generate/main.go:13](../lib/assets/generate/main.go#L13) copies HTML/SVG into Go constants. Embedding can remove this generator, but exported constants would become variables, and the SVG currently lives outside the asset package under `fixtures`. Resolve that API and layout tradeoff first: embed patterns cannot traverse `..`. Keep JS helper generation, which extracts functions and dependencies rather than merely copying bytes. [embed package](https://pkg.go.dev/embed).

- [ ] **G05 · P1 · Generate assertions that verify protocol behavior.**
  The generated event test template at [lib/proto/generate/main.go:232](../lib/proto/generate/main.go#L232) uses an empty regex, which matches every event name. Generate exact expected method/event-name assertions and verify command request/response routing where useful. Add a small schema fixture covering patches, optional fields, and deterministic formatted output. Favor assertions that detect incorrect generated behavior.

## Tests and benchmarks

- [ ] **T01 · P1 · Make benchmarks measure a defined operation and release resources promptly.**
  [lib/launcher/load_test.go:16](../lib/launcher/load_test.go#L16) always launches 300 browsers without using benchmark calibration. Turn it into an explicit load test, or define a repeatable operation using `b.Loop`; its random sleeps currently dominate elapsed time. [lib/benchmark/basic_test.go:19](../lib/benchmark/basic_test.go#L19) uses `RunParallel` correctly, but registers browser/profile cleanup with `b.Cleanup` inside every iteration, retaining resources until the entire benchmark ends. Clean up each iteration's resources inside that iteration and define whether cleanup is measured. Keep `RunParallel`/`pb.Next` for parallel benchmarks. [testing.B.Loop](https://pkg.go.dev/testing#B.Loop), [testing.B.RunParallel](https://pkg.go.dev/testing#B.RunParallel).

- [ ] **T02 · P2 · Base test work on native test contexts.**
  [internal/testutil/testutil.go:96](../internal/testutil/testutil.go#L96), [105](../internal/testutil/testutil.go#L105), and [113](../internal/testutil/testutil.go#L113) repeatedly create background contexts and cleanup cancellations. Use `t.Context()`/`b.Context()` as the parent, retaining explicit cancel/timeout wrappers where their callers need them. Native contexts cancel before cleanup starts; browser shutdown may require a separate bounded cleanup context. Update internal test doubles/contracts consistently. [testing.T.Context](https://pkg.go.dev/testing#T.Context).

- [ ] **T03 · P2 · Use fake time for isolated Go concurrency tests.**
  [lib/utils/utils_test.go:204](../lib/utils/utils_test.go#L204) includes an idle-counter timing assertion constrained to 400–450 ms. Retry/cancellation cases at [lib/utils/sleeper_test.go:35](../lib/utils/sleeper_test.go#L35) and delayed callbacks at [internal/testutil/testutil_test.go:113](../internal/testutil/testutil_test.go#L113) are additional candidates for `testing/synctest`. Use `synctest.Test`, `Wait`, and Go 1.27 `Sleep` to advance fake time and await quiescence. Keep real-browser/process timing tests outside this migration. [testing/synctest](https://pkg.go.dev/testing/synctest).

- [ ] **T04 · P3 · Use Go 1.27's in-memory HTTP test server where appropriate.**
  Go-only tests using the helper at [internal/testutil/http.go:29](../internal/testutil/http.go#L29) can benefit from `httptest.NewTestServer(t, handler)` and its associated `server.Client()`. Its default network is in memory and supports deterministic concurrency testing. Browsers and `http.DefaultClient` cannot reach that fake network; keep actual loopback listeners for browser-facing fixtures. The API also needs a real `testing.TB`, which affects the helper's mockable interface. [httptest.NewTestServer](https://pkg.go.dev/net/http/httptest#NewTestServer).

- [ ] **T05 · P2 · Distinguish disposable test files from retained artifacts.**
  Replace repository-relative random `tmp/` files at [lib/utils/utils_test.go:89](../lib/utils/utils_test.go#L89), [104](../lib/utils/utils_test.go#L104), [119](../lib/utils/utils_test.go#L119), and [135](../lib/utils/utils_test.go#L135) with `t.TempDir()`. Use `t.ArtifactDir()` for CDP logs currently managed by [setup_test.go:31](../setup_test.go#L31) and [setup_test.go:138](../setup_test.go#L138), screenshots at [page_test.go:721](../page_test.go#L721), and PDFs at [page_test.go:889](../page_test.go#L889). Artifact directories are retained with `-artifacts` and otherwise cleaned automatically; preserve any intentional failed-test-only retention policy explicitly. [testing.T.ArtifactDir](https://pkg.go.dev/testing#T.ArtifactDir).

- [ ] **T06 · P1 · Give the e2e example's browsers explicit lifetimes.**
  [lib/examples/e2e-testing/setup_test.go:21](../lib/examples/e2e-testing/setup_test.go#L21) launches a browser during package-variable initialization and provides no matching suite close. At [line 34](../lib/examples/e2e-testing/setup_test.go#L34), it creates an incognito browser but only closes its page. Use an explicit suite or lazy owner with teardown, and close each incognito browser context with test cleanup. Package initialization currently launches the browser even with `go test -run '^$'`; use compile-only checks when execution is not intended.

- [ ] **T07 · P2 · Replace the remaining reflective test suite with native subtests.**
  [lib/proto/a_utils_test.go:15](../lib/proto/a_utils_test.go#L15) is the only non-harness caller of `testutil.Each`, but it discovers both handwritten `T` methods and the generated protocol tests. Convert all those methods to native tests/table-driven `t.Run`, update [lib/proto/generate/main.go:203](../lib/proto/generate/main.go#L203), and regenerate [lib/proto/definitions_test.go](../lib/proto/definitions_test.go). Verify that the same cases still execute before removing the reflection-driven runner in [internal/testutil/each.go:17](../internal/testutil/each.go#L17) and its dedicated tests. Changing only the entry point would silently drop receiver-method coverage. Keep useful assertion and fixture helpers. [testing package](https://pkg.go.dev/testing).

- [ ] **T08 · P3 · Evaluate native goroutine-leak diagnostics alongside the existing checks.**
  [internal/goroutines/leak.go:16](../internal/goroutines/leak.go#L16) polls custom stack snapshots; [internal/goroutines/trace.go:117](../internal/goroutines/trace.go#L117) parses runtime output. Go 1.27's `runtime/pprof` `goroutineleak` profile may reduce some custom diagnostic work. It identifies certain permanently blocked goroutines, whereas the current code also checks goroutines still alive after a timeout and attributes ancestry. Any replacement must preserve the existing lifecycle and ancestry checks. [Go 1.27 goroutine leak profile](https://go.dev/doc/go1.27#goroutine-leak-profile).

- [ ] **T09 · P3 · Add focused native fuzz tests for pure parsing boundaries.**
  Candidates include bounded stack parsing at [internal/goroutines/trace.go:131](../internal/goroutines/trace.go#L131), JSON paths/values in [lib/jsonvalue/value.go](../lib/jsonvalue/value.go), and WebSocket frame decoding seeded from [lib/cdp/websocket_private_test.go:18](../lib/cdp/websocket_private_test.go#L18). Keep inputs/allocation bounded and avoid browser, network, or container launches inside fuzz targets. [Go fuzzing](https://go.dev/doc/security/fuzz/).

## JavaScript and development tooling

- [ ] **D01 · P3 · Finish the remaining JS declaration cleanup.**
  Five `var` declarations remain at [lib/js/helper.js:90](../lib/js/helper.js#L90), [91](../lib/js/helper.js#L91), [117](../lib/js/helper.js#L117), [392](../lib/js/helper.js#L392), and [413](../lib/js/helper.js#L413). Use `const` or `let` according to reassignment and scope, then regenerate `helper.go`. The source already uses modern functions, promises, and async/await extensively; avoid a broader syntax rewrite.

- [ ] **D02 · P2 · Keep monitor polling alive after a failed request.**
  [lib/assets/monitor.html:40](../lib/assets/monitor.html#L40) only schedules the next update after successful fetch, JSON parsing, and rendering. A rejection stops polling permanently. Check the response status, handle the failure, and reschedule in a deliberate cleanup/finally path, following the existing retry behavior in [lib/assets/monitor-page.html:89](../lib/assets/monitor-page.html#L89). Update generated assets together with the HTML source.

- [ ] **D03 · P2 · Establish verification for every module and generation boundary.**
  Define CI checks for Go 1.27.1: formatting, `go vet`, reviewed `go fix -diff` suggestions, race tests, and deterministic generation checks. [go.work:3](../go.work#L3) contains four modules; root `./...` excludes the three nested modules. Check them explicitly, and verify workspace versus `GOWORK=off` resolution. Preserve local replacement directives in the nested modules. Keep browser, live-site, and Docker/image-pull checks visibly separated from pure checks. [Go command package patterns](https://pkg.go.dev/cmd/go#hdr-Package_lists_and_patterns), [cmd/fix](https://pkg.go.dev/cmd/fix).

- [ ] **D04 · P3 · Reconcile stale comments and suppressions with the new tooling.**
  [lib/js/js.go:8](../lib/js/js.go#L8) still claims helpers are compressed by uglify-js, but [lib/js/generate/main.go:58](../lib/js/generate/main.go#L58) uses Node and function serialization. Update generation documentation and commands when the corresponding changes land. Review orphaned `nolint`/spelling directives against the chosen checks, retaining explanations that still capture a real exception. Root generation directives already exist at [browser.go:1](../browser.go#L1); extend their documented workflow as needed instead of introducing duplicate entry points.

## Optional API and implementation changes

- [ ] **O01 · P3 · Add a typed single-event API if callers need it.**
  [browser.go:359](../browser.go#L359) and [page.go:682](../page.go#L682) accept heterogeneous callbacks and use runtime reflection. Go 1.27 generic methods, or generated typed wrappers, could provide a simpler single-event path. They do not automatically replace heterogeneous `EachEvent` behavior. Interface methods cannot declare their own type parameters, and generic methods cannot implement interface methods. Treat this as an API design task with caller examples, not part of the `any` replacement. [Go 1.27 language changes](https://go.dev/doc/go1.27#language).

- [ ] **O02 · P3 · Consider typed storage/equality only where it simplifies current code.**
  The homogeneous pending-request store at [lib/cdp/client.go:51](../lib/cdp/client.go#L51) could use a typed map and mutex to remove forced assertions; call callbacks outside the map lock and measure contention. Browser state at [states.go:23](../states.go#L23) mixes different responsibilities and needs a separate design review. Shape equality at [element.go:545](../element.go#L545) and [577](../element.go#L577) could use explicit typed comparison, but must preserve nil/empty and floating-point behavior. Benchmark these changes before treating them as performance improvements.

## Project rules and compatibility constraints

- Always use the standard `encoding/json` package with its default behavior. Direct adoption of `encoding/json/v2` is prohibited and is not a future migration option. Go 1.27's newer implementation behind `encoding/json` is an internal implementation detail; it does not change this project rule. JSON-related modernization, including R07, must stay within `encoding/json`. [Go 1.27 JSON changes](https://go.dev/doc/go1.27#encoding-json-v2).
- Preserve optional protocol pointers and the distinction between omitted fields and explicit zero/false/empty values. In particular, [lib/proto/generate/patch.go:76](../lib/proto/generate/patch.go#L76) deliberately handles empty versus absent response bodies.
- Keep SHA-1 in [lib/cdp/websocket.go:6](../lib/cdp/websocket.go#L6): it belongs to the WebSocket handshake protocol, so changing the hash would break interoperability.
- Do not blindly change [lib/launcher/launcher.go:430](../lib/launcher/launcher.go#L430) to `exec.CommandContext(l.ctx, ...)`. `Launch` defers cancellation of that context at [line 409](../lib/launcher/launcher.go#L409), so doing so would kill the browser when launch returns successfully. Any `Cmd.WaitDelay` change also needs an explicit process/pipe-drain policy.
- Preserve deliberately shared pointer mutexes/maps in Browser/Page clones; [context.go:19](../context.go#L19) and [context.go:56](../context.go#L56) copy their outer structs. Converting all locks to value fields would change sharing or copy used locks.
- Do not replace `utils.RandString(n)` with `crypto/rand.Text()` without auditing consumers: the former returns `2*n` hexadecimal characters; the latter has a different length/alphabet contract. `crypto/rand.Text` is already used in manager code.
- `min`/`max` look attractive in [lib/proto/a_patch.go:136](../lib/proto/a_patch.go#L136), but floating-point NaN/signed-zero handling can change. Similarly, `slices.Equal` treats nil and empty slices as equal where `reflect.DeepEqual` does not.
- `log.Logger`, `sync.Once`, `sync.Map`, channels, `&T{}`, and small private helpers are not inherently obsolete. Adopt `slog`, iterators, generic abstractions, or different synchronization only for a demonstrated benefit.
- `lib/js/helper.js` deliberately uses `XMLHttpRequest` with its own response/error behavior; replacing it with `fetch` is not a syntax-only change. Keep current safe DOM construction in the monitor.
- The manual unset-environment test in [lib/defaults/defaults_test.go:18](../lib/defaults/defaults_test.go#L18) must distinguish an absent key from an empty value; `t.Setenv(key, "")` is not equivalent.

The code already uses generic `Pool[T]` and observables, typed atomics, `bytes.Clone`, `os.ReadDir`, `io.ReadAll`, native test cleanup/temp/environment helpers, `os.Root`, and `//go:build` constraints. These need no migration. There are no `io/ioutil` imports, old `// +build` directives, or `rand.Seed` calls to replace.

## Suggested implementation order

1. Fix the P1 lifecycle and generator-ownership problems with focused behavior checks.
2. Establish generated-file markers; update templates and apply the mechanical idioms in small coherent batches.
3. Migrate the test harness, timing tests, artifacts, and benchmarks, then establish all-module checks.
4. Handle JSON presence semantics and optional public API/storage/embedding work independently, with explicit compatibility decisions.

## Validation

Each change should include formatting checks, vet, and relevant tests. Check the root module both with and without the workspace, and include nested modules explicitly:

```sh
go vet -mod=readonly ./...
GOWORK=off go vet -mod=readonly ./...
go vet -mod=readonly ./lib/docker/... ./lib/examples/custom-websocket/... ./lib/examples/e2e-testing/...
```

Use `go fix -diff -mod=readonly ./...` to preview automated suggestions. A nonempty diff produces exit status 1. Review changes to generators and JSON tags before applying them.

Run relevant tests with `-count=1 -race -cover -covermode=atomic`. Browser and container test setup is documented in the [README](../README.md#development). The container compatibility test intentionally pulls the latest browser image on each run.

### Completed validation

**R01 — TLS cancellation, 2026-09-05:** the regression test reproduced the stalled-handshake failure before the adapter was replaced. Cancellation before dialing and during a TLS handshake now passes with the race detector enabled. Package vet also passed.

```sh
go test -mod=readonly -count=1 -race -cover -covermode=atomic -run '^(TestWebSocketTLSCancellation|TestWebSocketErr|TestWebSocketHeader)$' ./lib/cdp
```
