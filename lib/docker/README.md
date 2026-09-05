# Container integration test

`docker_test.go` builds `rod-manager` and its integration client locally,
copies them into `docker.io/chromedp/headless-shell:latest`, starts the
authenticated manager, exercises that image's Chromium through Rod, and
removes the container through Testcontainers.

The floating `latest` tag is intentional: `WithAlwaysPull` fetches the current
image on every run so this test acts as a compatibility canary for the latest
stable Chromium instead of a reproducible browser-version test.

The test disables Testcontainers' Ryuk sidecar to avoid pulling a second
container image and registers the Chromium container with `CleanupContainer`
for removal. If the test process is forcibly terminated before Go can run test
cleanup, the container provider may retain that test container.

The manager keeps its loopback-only default and no container port is exposed.
Testcontainers executes the integration client inside the same container. The
client verifies that an unauthenticated request is rejected before connecting
with an ephemeral bearer token.

Run it from the repository root with `go.work` enabled:

```sh
go test -count=1 -race -cover -covermode=atomic ./lib/docker
```

The test is a repository-workspace module. It requires a Linux-container
provider on `amd64` or `arm64`. It skips in short mode, when the provider is
unavailable, or when the current architecture has no published image.

Rod does not invoke the Docker CLI, build an image, publish an image, or
download a browser at runtime. Testcontainers pulls and owns the browser
container lifecycle. The intentionally mutable Chromium image is the only
container image requested by this test.
