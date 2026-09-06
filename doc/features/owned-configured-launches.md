# Own configured local browser launches

Priority: P1. Status: proposed.

## Value and current support

Selecting an executable, setting launch flags, and capturing process output
currently leads callers to launch a browser themselves and pass its control URL
to Rod. They must then coordinate two lifetimes: the Rod connection and the
launcher process. Error paths and expired operation contexts make that cleanup
easy to get wrong.

[Browser.Connect](../../browser.go) already owns a process that it launches
automatically. Its internal process owner handles launch/connect failures and
is invoked by `Browser.Close`. [Launcher](../../lib/launcher/launcher.go) already
supports explicit configuration, installed-browser discovery, temporary
profiles, output logging, killing, and cleanup.

The remaining gap is ownership for an explicitly configured launch, together
with a bounded public cleanup contract. Connecting through `ControlURL` does
not transfer launcher ownership. Owned `Browser.Close` now has an independent
five-second graceful shutdown budget and forces cleanup when it expires. The
subsequent process cleanup wait and `Launcher.Cleanup` still have no cancellation
or error result.

## Proposed behavior

Add one entry point that launches an unlaunched, configured local `Launcher`,
connects a `Browser`, and retains ownership of the process it actually starts.
Reuse the existing process owner rather than introducing a runner or parallel
browser abstraction. Reject conflicting connection configuration before
launching. Keep ordinary URL attachment available with its existing semantics.

The owned path must either create a new process or return an explicit error.
It must not silently adopt a browser discovered on a configured debugging port.
Process termination and profile deletion apply only to resources created by
this launch. Caller-supplied profile directories remain caller-owned.

On startup or connection failure, roll back every owned resource. Preserve the
original error with wrapping, and retain cleanup errors when they are useful.
Allow bounded capture of recent stdout/stderr for launch and connection errors,
including failures after the DevTools endpoint was printed. Capture must be
safe for concurrent writes and must have an explicit size limit. Preserve the
existing logger integration; do not add a logging dependency or enable persistent
process-output storage by default.

Offer an error-returning shutdown path with a finite cleanup budget independent
of the operation context. Attempt graceful browser close, terminate the owned
process if needed, wait for exit within that budget, then remove owned temporary
profile data. Return a cleanup error if completion cannot be established. An
expired navigation deadline must not prevent cleanup from starting, and a
timeout must not merely abandon an indefinitely waiting cleanup goroutine.

Process cleanup must be safe to request repeatedly and from cancellation/error
paths. Closing an incognito context must continue to dispose only that context.
Retain the platform limits described in the
[launcher documentation](../../lib/launcher/README.md); this feature does not
promise cleanup after a host crash or portable termination of every descendant.

## Related work

The [launcher reuse cleanup task](../pulls/1221-launcher-reuse-cleanup.md)
is implemented and covers a standalone launcher waiting on a process it did not
start. This proposal adds the public composition for a configured launch.

Automatic browser discovery, fresh default profiles, and automatic-launch
ownership are existing functionality and should be reused.

## Acceptance

- A configured executable and launch options take effect without global default
  mutation. The returned browser owns exactly the process started by the call.
- Cover executable-not-found, launch failure, cancellation during startup,
  invalid DevTools endpoint, connection failure, and successful connection.
- Verify owned profile cleanup after partial startup and failed connection;
  preserve caller-supplied profile contents on all paths.
- An already occupied debugging port never causes adoption or termination of an
  unrelated browser. Reject an already-used launcher and conflicting connection
  options without changing ownership.
- After an operation deadline expires, shutdown still attempts graceful close
  and process cleanup. A stalled close or exit wait returns within the specified
  cleanup budget with an actionable error and no abandoned waiter.
- Repeated cleanup, an already exited process, and concurrent shutdown requests
  do not panic or terminate unrelated processes. Incognito close leaves its
  parent browser running.
- Error output retains the underlying cause and a bounded output tail, including
  concurrent stdout/stderr writes and empty output.
- Use deterministic process/connection failure fixtures for lifecycle checks;
  verify successful launch, close, and profile removal with an installed browser.
