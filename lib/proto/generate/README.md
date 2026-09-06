# Protocol generation

Run from the repository root:

```sh
go run ./lib/proto/generate
```

Generation reads the checked-in `schema.json` without launching a browser or using
the network. `schema-provenance.json` records Chromium revision 1321438,
Chrome 128.0.6568.0, and the SHA-256 digest. This snapshot reproduces the protocol
declarations from upstream generation commit `5098fbe03be30abb8cb044b27985633d67775027`.
Generated headers include the schema digest.

An explicit snapshot and output directory can be selected:

```sh
go run ./lib/proto/generate -schema /tmp/protocol.json -out /tmp/proto-output
```

The output directory must already exist. The generator renders and formats every
file before replacement, recognizes only its own generated-file headers, refuses
handwritten filename collisions, and removes only obsolete owned files. It stages
output and backs up existing files during replacement. If filesystem errors also
prevent rollback, the error reports the retained backup directory for recovery.
Do not run multiple generators against the same output directory concurrently.

To update the protocol deliberately, launch the selected Chromium build with a
local debugging port, capture `/json/protocol` and `/json/version` using a bounded
HTTP request, and update the snapshot and provenance together. For example:

```sh
curl --fail --max-time 30 http://127.0.0.1:9222/json/protocol -o /tmp/protocol.json
curl --fail --max-time 30 http://127.0.0.1:9222/json/version -o /tmp/browser-version.json
sha256sum /tmp/protocol.json
```

Record the exact browser revision/version and schema digest; omit the ephemeral
`webSocketDebuggerUrl`. Review changed APIs, optional field types, and wire behavior
before accepting the new generated output. The default `encoding/json` contract
is required: optional command booleans use `*bool`, so nil, false, and true remain
distinct. Other optional pointers stay pointers. `RuntimeCallArgument.Value` uses
`omitzero` to omit an unset value while preserving explicit `jsonvalue.New(nil)`
as null; other `jsonvalue.Value` fields remain present as null when unset.
The Fetch body patch preserves the difference between
`nil` (`null`) and an empty byte slice (`""`).

```sh
go test -race ./lib/proto/...
go run ./lib/proto/generate
git diff --exit-code -- lib/proto
```

The fixture tests cover patches, optional fields, exact command/event names,
formatted deterministic output, ownership collisions, and failed generation.
The protocol tests use native `TestName` functions for all 800 former suite cases.
