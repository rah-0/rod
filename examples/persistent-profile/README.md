# Persistent automation profile

Use an explicit, non-default directory to retain browser state between automation
runs. The directory belongs to you and remains after browser cleanup.

Chrome 136 and later ignore remote-debugging switches when using the default
Chrome data directory. Closing Chrome alone does not remove this restriction;
see [Chrome's announcement](https://developer.chrome.com/blog/remote-debugging-port).
`launcher.NewUserMode` keeps its legacy behavior for browsers that support it,
but does not bypass this restriction.

Create a separate automation profile and establish any needed site logins there.
For example, using an installed Chromium executable on Linux:

```sh
chromium --user-data-dir="$HOME/.config/rod-automation-profile"
```

Sign in to the sites needed for automation in that browser, then close it before
running the example from the repository root:

```sh
go run ./examples/persistent-profile -profile "$HOME/.config/rod-automation-profile" -rod=bin=chromium
```

Use the installed browser executable and a dedicated profile path appropriate
for your platform. The example discovers an installed browser, starts it
headlessly with `UserDataDir`, opens a blank page in its default browser context,
and closes it with a bounded cleanup budget. Use the same browser and directory
for login setup and later automation. To select the executable explicitly, pass
`-rod=bin=/path/to/browser`, which configures `Launcher.Bin`. Only one browser
should use that directory at a time.

The profile does not inherit the personal browser's cookies or login sessions.
Rod does not copy a personal profile or recover its sessions. Logins established
in the automation profile can be reused according to the site's cookie expiry
and session policy. `Cleanup`, `CleanupContext`, and an owning `Browser.Close`
preserve the supplied directory.
