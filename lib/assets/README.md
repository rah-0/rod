# Assets

Static files for the project. Edit `monitor.html`, `monitor-page.html`, or
`mouse-pointer.svg` in this package. Go embeds their bytes into the exported string
variables `Monitor`, `MonitorPage`, and `MousePointer` at build time.

Validate from the repository root:

```sh
go test ./lib/assets/...
```

The monitor checks response status and retries polling after request, JSON, or
rendering failures. Its polling behavior test requires Node.js; target data is
rendered using DOM text properties. Monitor pages request the API and
screenshots with relative URLs, so they stay below the access-token path that
`Browser.ServeMonitor` returns.
