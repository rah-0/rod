# Examples

Each example contains runnable code and a test; additional usage notes live
beside the code. Examples start their own browsers and local services.

Run commands from the repository root:

```sh
go run ./examples/forms
go run ./examples/drag
bash scripts/check.sh examples
```

The check command runs all examples, including the separate custom-WebSocket and
E2E test modules. The E2E project runs through its tests rather than `go run`.
