# Protocol generation

Run from the repository root:

```sh
go run ./lib/proto/generate
```

Generation launches the installed Chrome, Chromium, or Edge browser in a private
temporary profile and reads its `/json/protocol` and `/json/version` endpoints.
The launcher finds the executable using the same discovery as browser tests.
There is no fixed browser version and no fallback to the recorded schema when
launching or reading the browser fails. Keep the browser installation updated.

Select a particular executable when needed:

```sh
go run ./lib/proto/generate -bin /usr/bin/chromium
```

`-timeout` bounds capture, and `-no-sandbox` explicitly disables the browser
sandbox for environments that require it. Browser processes and temporary
profiles are cleaned up after capture, including failures.

Generated Go files remain checked in, so ordinary builds do not launch Chrome.
`schema.json` and `schema-provenance.json` record the generation input, canonical
schema digest, and browser metadata when captured live. They describe the generated
bindings; they do not select or constrain the next browser. Formatting and JSON
object key order do not affect the digest. `schema-compatibility.json` records
the members that older browsers omit; see [required members](#required-members).

## Freshness and compatibility

Check the installed browser against the generated bindings without modifying
files:

```sh
go run ./lib/proto/generate -check
bash scripts/check.sh browser
```

The freshness check prints the actual browser version and fails when generated
files differ, are missing, or are obsolete. A browser version change alone does
not fail the check when the protocol is unchanged. The browser test script also
runs runtime tests when the freshness check fails, so both protocol changes and
failures in exercised APIs are visible.

If the environment requires disabling sandboxing for protocol capture, opt in
with `ROD_PROTOCOL_NO_SANDBOX=1 bash scripts/check.sh browser`. Direct generation
and checks accept `-no-sandbox`; sandboxing is enabled by default.

When the check reports drift, regenerate, review the API changes, adapt affected
code, and rerun validation. Protocol freshness does not prove every CDP command
works: runtime coverage is still necessary.

## Explicit offline input

Use `-schema` to deliberately render a saved protocol without launching a
browser. This supports offline reproduction and focused generator fixtures:

```sh
mkdir -p /tmp/proto-output
go run ./lib/proto/generate -schema lib/proto/generate/schema.json -out /tmp/proto-output
```

An alternate output directory must already exist. Live generation stores its
schema records under that directory's `generate` subdirectory; selecting an
alternate output does not update the repository's records.
Offline generation also replaces these records, labels the source as an explicit
schema file, and omits browser metadata because it did not inspect a browser.
Generation reads the previous `schema.json` and `schema-compatibility.json` from
the same subdirectory. To reproduce the repository's bindings in another
directory, copy `schema-compatibility.json` into its `generate` subdirectory
first.

## Output safety and wire contracts

The generator renders and formats every file before replacement, recognizes
only its own generated-file headers, refuses handwritten filename collisions,
and removes obsolete owned files. It stages output and backs up existing files
during replacement. If filesystem errors also prevent rollback, the error
reports the retained backup directory for recovery. Do not run multiple
generators against the same output directory concurrently.

Review changed APIs, optional field types, and wire behavior before accepting
new output. The default `encoding/json` contract is required: optional command
booleans use `*bool`, so nil, false, and true remain distinct. Other optional
pointers stay pointers. `RuntimeCallArgument.Value` uses `omitzero` to omit an
unset value while preserving explicit `jsonvalue.New(nil)` as null; other
`jsonvalue.Value` fields remain present as null when unset. The Fetch body patch
preserves the difference between `nil` (`null`) and an empty byte slice (`""`).

The generator imports packages that use the generated bindings. Build a generator
executable before a large protocol update if it may break those packages; the
executable remains usable while consumer code is being migrated:

```sh
go build -o /tmp/rod-proto-generate ./lib/proto/generate
/tmp/rod-proto-generate
go test -count=1 -race -cover -covermode=atomic ./lib/proto/...
```

Small deterministic fixture tests cover patches, optional fields, required
members and the compatibility record, exact command/event names, formatted
output, ownership collisions, and failed generation. They do not set the
browser version used for generation or tests.

## Required members

`decoders.go` holds the `decodeJSON` methods that `proto.Unmarshal` and the
generated `Call` methods use. Command results, events, and the structs they can
contain get a method; command parameters are only sent and have none. Each
method decodes an object in one pass, records which required members it has
seen, and reports the first one that is missing. A member is required unless the
schema marks it optional, experimental, or deprecated, or
`schema-compatibility.json` lists it. Browsers older than the schema can lack
experimental and recently added members, and newer ones can drop deprecated
members, so those members are decoded when present but never required. Their Go
types stay unchanged.

`schema-compatibility.json` lists the members, as `Domain.definition.member`
keys, that browsers older than the schema omit. A definition is a type, an
event, or a command name followed by `Result`, as for `proto.GetType`.
Generation adds the members that the new schema does not mark optional and the
previous `schema.json` lacks or marks optional, in definitions that the
previous schema has. In an older schema, a command without `returns` has a
result definition without members, because the browser answers it with an empty
object, and an object type without `properties` is a definition without
members. Definitions that older browsers do not have need no entry, because
those browsers never send them. The `-compat` flag compares the schema
with the protocol of an older browser in the same way, which extends support to
that browser:

```sh
go run ./lib/proto/generate -compat /path/to/older/protocol.json
```

Save the older protocol from that browser's `/json/protocol` endpoint. The
record includes the members that Chromium 128 lacks. Members stay listed until
the schema drops them or marks them optional, and their documentation starts
with `(optional in older browsers)`. A browser older than every protocol that
generation has compared can omit other members; decoding its data returns
`proto.ErrMissingField` until generation compares its protocol with `-compat`.

Each generated command test answers with the smallest result that has every
required member; generation fails when required objects form a cycle, because
such a result would be infinite, and when a struct has more than 64 required
members.
