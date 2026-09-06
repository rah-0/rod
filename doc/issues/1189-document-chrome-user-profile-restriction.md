# Reconcile NewUserMode with Chrome's default-profile debugging restriction

Priority: P2 · Compatibility documentation · Confirmed from source inspection and official Chrome guidance on 2026-09-05.

Source: [upstream issue #1189](https://github.com/go-rod/rod/issues/1189).

[NewUserMode](../../lib/launcher/launcher.go) advertises reuse of the personal browser's current profile and recommends closing an existing browser when debugging fails. It supplies a debugging port but no separate user-data directory. [The Chrome announcement](https://developer.chrome.com/blog/remote-debugging-port) states that from Chrome 136 remote-debugging switches are ignored for the default Chrome data directory; debugging requires a non-default `--user-data-dir`. Closing Chrome alone does not remove that restriction.

Document this limitation in `NewUserMode`, [the launcher README](../../lib/launcher/README.md), and relevant [browser examples](../../examples). Show an explicit persistent automation profile through `UserDataDir`, including that the user must establish a login in that separate profile. Preserve existing user-supplied profile ownership and the installed-browser-only policy.

Preserve Chrome's existing restriction diagnostic. Do not silently copy a personal profile, promise existing sessions can be recovered, or bypass Chrome's restriction. Keep legacy-compatible user-mode behavior available for browsers that support it; this task does not require a generic browser-version detection layer.

Acceptance criteria:

- Public guidance no longer promises personal default-profile reuse on affected Chrome versions or suggests closing the browser is sufficient.
- The example uses an explicit caller-owned persistent automation directory and states its session semantics.
- The example compiles against the fork and uses the existing supported launcher APIs.
- An explicitly selected non-default profile continues to be preserved by `Cleanup`.
