# Make SetBlockedURLs take effect on a fresh page

Priority: P2. Source: [issue #1168](https://github.com/go-rod/rod/issues/1168), closed upstream after identifying an undocumented setup requirement.

Reviewed: 2026-09-05.

## Current evidence

[Page.SetBlockedURLs](../../page.go) calls `Network.setBlockedURLs` without enabling the Network domain. Its public documentation does not mention a prerequisite, and the method can return success while matching traffic continues normally.

Reproduced with Chrome 152 and a local HTTP fixture: set a blocked URL on a fresh page, then request that URL from page JavaScript. The request succeeds. With the same setup but an explicit `Network.enable` call before `SetBlockedURLs`, the request is blocked. The upstream author identified the same missing setup in the closing discussion.

## Scope

Ensure the high-level helper establishes the Network-domain state required for its configured rules to work, and propagate setup/configuration errors. Preserve existing URL pattern and empty-list behavior unless a separately justified compatibility decision changes it. Do not immediately restore/disable the domain after configuration if doing so would make the rules ineffective. Document how long the configured blocking remains active and how callers can clear it using supported APIs.

## Acceptance

- On a fresh page with no Network listeners, configuring a matching URL blocks that request without additional caller setup.
- Nonmatching requests continue normally; configuration survives navigation as specified.
- Existing Network users keep working, and setup/configuration failures are returned.
- Use a deterministic local HTTP fixture and verify the server never receives the blocked request.
