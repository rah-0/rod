# Forms and uploads from memory

Run from the repository root:

```sh
go run ./examples/forms
```

The [example](main.go) launches a headless browser and serves a local HTTP
fixture. It opens a hidden form, fills its title, uploads `note.txt` from
memory, and submits a multipart POST. It verifies the received filename,
content, and byte count. The [test](main_test.go) runs the same code and checks
the result.

`Element.SetFilesFromMemory` sends filenames, MIME types, and bytes to local
or remote browsers through the existing CDP connection, without temporary
files. Nil or empty lists clear the selection; selecting multiple files
requires an input with `multiple` enabled. Assignment replaces the file list
and dispatches synthetic `input` and `change` events (`isTrusted` is false).
Filenames and MIME types follow the browser's `File` constructor semantics.
`MustSetFilesFromMemory` accepts variadic payloads and panics on errors.
`Element.SetFiles` accepts paths that must exist on the browser's host. It
makes relative paths absolute on the client and does not transfer file
contents.
