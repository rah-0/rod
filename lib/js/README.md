# JavaScript helpers

Edit `helper.js`, then run from the repository root with Node.js installed:

```sh
go run ./lib/js/generate
go test ./lib/js/...
```

The generator uses Node.js function serialization and extracts references to
other helpers into their dependency lists. It formats the complete Go source
before replacing `helper.go`. Generation uses standard input, has a 30-second
process timeout, and does not install Node.js or create repository temporary files.
