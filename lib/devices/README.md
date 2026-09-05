# Device profiles

Regenerate profiles from the repository root:

```sh
go run ./lib/devices/generate
```

Generation is offline. `generate/devices.json` is the immutable DevTools device
source pinned by `deviceListURL` in `generate/main.go`; its SHA-256 digest is checked
before parsing. Generated headers record the URL, checksum, and the deliberate
Chrome user-agent substitution version, currently `114.0.0.0`. The source pin
and this version determine the generated profiles and user agents.

To update profiles, choose an immutable DevTools commit, download its device list
with a bounded request, inspect its JSON structure, and update the checked-in
snapshot, `deviceListURL`, `deviceListSHA`, and any necessary parser changes
together. Review `chromeVersion` explicitly when updating the source; it controls
both `%s` substitution and the desktop fallback. Do not replace the source with an
unversioned latest URL.

```sh
go test -race ./lib/devices/...
go run ./lib/devices/generate
git diff --exit-code -- lib/devices
```

Use `-source` to select another copy of the pinned snapshot and `-out` to select an
output file. Checksum or formatting failures leave the existing output intact.
