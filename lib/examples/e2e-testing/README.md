# Overview

This is a sample project to demonstrate how to use Rod to setup an end-to-end testing (e2e testing) project.
The test cases run in parallel and share a browser that starts when the first test needs it and closes after the suite. Each test owns an incognito browser context that closes during cleanup.

Use `go test` to execute all tests. `go test -run '^$'` checks test discovery without launching a browser.

## Debugging

Use Rod's trace, slow-motion, and DevTools options to inspect a test run.
