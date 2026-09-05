# Overview

A lib that helps to find and launch a locally installed browser. You can also use it as a standalone lib without Rod.

Rod doesn't download browser binaries. Install Chrome, Chromium, or Edge yourself, or provide an explicit executable path with `Launcher.Bin`.

Local launchers execute the browser directly. Call `Browser.Close` or
`Launcher.Kill` during normal shutdown; an abrupt process or host failure does
not provide a portable cleanup guarantee. The remote manager owns each browser
and its randomly named profile, and terminates the directly launched browser
when that WebSocket session ends. On Unix it also targets the browser process
group; Windows child-process cleanup remains browser-dependent.

`Launcher.Cleanup` removes launcher-generated temporary profiles after the
browser exits. A profile supplied through `UserDataDir` remains caller-owned.

## Remote manager security

`rod-manager` requires a bearer token from `ROD_MANAGER_TOKEN` and listens on
`127.0.0.1:7317` by default. Pass the same token explicitly to `NewManaged`.
The command removes the token from its process environment after reading it,
and the manager strips that variable from every browser child environment. The
library-level `NewManager` is also locked when given an empty token.

Use HTTPS/WSS, a trusted TLS reverse proxy, or an encrypted tunnel before
binding the manager to a network interface. A bearer token sent over plain
HTTP/WebSocket can be intercepted. Managed clients reject plaintext
non-loopback URLs, and the command requires an explicit
`-allow-plaintext-remote` override for non-loopback listeners.

Treat every token holder as an administrator of the browser host. Managed
clients have full Chrome DevTools access. Process-owned launch settings,
including the browser executable, environment, working directory, and XVFB
wrapper, cannot be changed remotely. `Launcher.XVFB` remains available to
trusted local launchers. Managed clients also cannot choose the profile path or
debugging port; cleanup is confined to the manager-created profile.
