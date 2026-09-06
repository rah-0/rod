# Platform support and validation

Last reviewed: **2026-09-06**.

For version-specific changes and migration instructions, see
[Breaking changes](BREAKING.md).

## System matrix

This matrix covers **locally launched browsers: automation, shutdown, and cleanup
after application failure**. CPU names use Go's `GOARCH`: `amd64` means x86-64
(Intel or AMD), `arm64` means 64-bit ARM, and `386` means 32-bit x86.

- **Validated:** native browser tests and failure-cleanup checks passed in the exact recorded environment.
- **Expected compatible:** validation on a comparable environment supports compatibility; this is an assessment, not a separate passing test.
- **Unverified:** no native runtime result; compatibility needs further assessment.
- **Limited:** a known implementation gap prevents equivalent cleanup or default launch behavior.

“Compilation passed” refers only to `lib/launcher`, without executing it.
Chromium binary sources are linked below; availability was checked on
**2026-09-06**.

| System | CPU architecture | Status | Evidence and limitations |
| --- | --- | --- | --- |
| Debian GNU/Linux **13.6 (trixie)** | `amd64` | **Validated** | Native headless Chrome automation and failure cleanup passed. See the [exact environment](#validated-environment) and [test coverage](#validation-coverage). |
| Linux: comparable glibc-based distributions, including Ubuntu | `amd64` | Expected compatible | Debian validation gives strong evidence for the same architecture with a comparable installed browser and the Linux prerequisites below. Other distributions have not been tested directly. |
| Linux | `arm64` | Unverified | [Debian Chromium packages](https://packages.debian.org/trixie/chromium) are available. Expected to work with the Linux prerequisites; no Rod build or runtime check recorded. |
| Linux | `386`, `arm` (ARMv7/armhf) | Unverified | [Debian Chromium packages](https://packages.debian.org/trixie/chromium) are available for `i386` and `armhf`. No Rod build or runtime check recorded; the standard race-enabled test runner cannot run on these targets. |
| Linux | `ppc64le`, `loong64`, `riscv64` | Unverified | Browser binaries are available through [Debian (`ppc64el`)](https://packages.debian.org/trixie/chromium), [Debian sid (`loong64`)](https://packages.debian.org/sid/chromium), and a [community RISC-V port](https://github.com/riscv-forks/chromium-riscv/releases/tag/152.0.7977.75). No Rod build or runtime check recorded. |
| macOS | `amd64` | Limited | [Chromium builds](https://commondatastorage.googleapis.com/chromium-browser-snapshots/index.html?prefix=Mac/) are available. Launcher compilation passed; automation is untested and detached helpers can escape process-group cleanup. |
| macOS | `arm64` | Limited | [Chromium builds](https://commondatastorage.googleapis.com/chromium-browser-snapshots/index.html?prefix=Mac_Arm/) are available. No Rod build or runtime check recorded; the same process-group cleanup limitation applies. |
| Windows | `amd64` | Limited | [Chromium builds](https://commondatastorage.googleapis.com/chromium-browser-snapshots/index.html?prefix=Win_x64/) are available. Launcher compilation passed; automation is untested and application death can leave browser processes and profiles behind. |
| Windows | `arm64`, `386` | Limited | Chromium builds are available for [ARM64](https://commondatastorage.googleapis.com/chromium-browser-snapshots/index.html?prefix=Win_Arm64/) and [x86](https://commondatastorage.googleapis.com/chromium-browser-snapshots/index.html?prefix=Win/). No Rod build or runtime check recorded. Same application-death cleanup gap; the standard race-enabled test runner cannot run on these targets. |
| FreeBSD | `amd64` | Limited | Chromium packages are published for [FreeBSD 14](https://pkg.freebsd.org/FreeBSD:14:amd64/quarterly/packagesite.pkg) and [15](https://pkg.freebsd.org/FreeBSD:15:amd64/latest/packagesite.pkg). Launcher compilation passed. Browser discovery supports `chrome` and `chromium`; native automation and cleanup are untested, with weaker process-group cleanup than Linux. |
| OpenBSD | `amd64`, `arm64` | Limited | Chromium packages are published for [amd64](https://cdn.openbsd.org/pub/OpenBSD/snapshots/packages/amd64/) and [aarch64](https://cdn.openbsd.org/pub/OpenBSD/snapshots/packages/aarch64/). Browser discovery is implemented. No Rod build or runtime check recorded; process-group cleanup is weaker than Linux. |
| NetBSD | `amd64` | Limited | A Chromium 149 binary is published in the [NetBSD 10 x86-64 repository](https://cdn.netbsd.org/pub/pkgsrc/packages/NetBSD/x86_64/10.0_2026Q2/All/). Provide `Launcher.Bin`. No Rod build or runtime check recorded; same Unix cleanup limitation. |
| DragonFly BSD | `amd64` | Limited | The [DragonFly 6.4 repository](https://mirror-master.dragonflybsd.org/dports/dragonfly:6.4:x86:64/LATEST/All/) publishes an **older Chromium 137 build**. Current-browser compatibility is unverified. Provide `Launcher.Bin`; no Rod build or runtime check recorded, with the same Unix cleanup limitation. |

Rod requires an installed browser that meets its vendor's
[system requirements](https://support.google.com/chrome/answer/95346?hl=en).

## Linux compatibility

Linux distributions share a deliberately stable
[kernel-to-userspace interface](https://www.kernel.org/doc/html/latest/process/stable-api-nonsense.html).
Expected compatibility assumes the same CPU architecture, a comparable browser
installation, and access to the required process-management facilities.

Relevant environment differences include:

- **CPU architecture:** `amd64` results do not validate `arm64`, even though both are 64-bit.
- **System libraries:** [Alpine uses musl](https://www.alpinelinux.org/about/); its browser environment is outside the Debian/glibc validation.
- **Browser packaging:** confined packages, such as [Chromium distributed as a Snap](https://ubuntu.com/blog/chromium-in-ubuntu-deb-to-snap-transition), can change browser access to files and processes.
- **System restrictions:** containers or hardened hosts can restrict the `/proc` access and supervision required by the launcher.

## Validated environment

| Component | Recorded value |
| --- | --- |
| Tested Rod version | **v0.119.0** |
| Validation date | **2026-09-06** |
| Operating system | **Debian GNU/Linux 13.6 (trixie)** |
| Kernel | **Linux 6.12.100+deb13-amd64** |
| Architecture | **linux/amd64**, native x86-64 execution |
| C library | **glibc 2.41**, Debian package `2.41-12+deb13u3` |
| Browser | **Google Chrome 151.0.7922.71**, headless |
| Go toolchain | **Go 1.27.1**, with the race detector for runtime test suites |
| Host execution | Linux host processes, monitored in a dedicated cgroup; no Docker browser image used |
| User and browser sandbox | Failure matrix run as an unprivileged user with Chrome sandboxing enabled and as root with `--no-sandbox`; the root-package test helper also uses `--no-sandbox` |
| Browser test scheduling | Packages and cases sequential (`-p=1 -parallel=1`); root suite reuses one cached browser and retires it after a failed test |

These are the tested versions, not minimum system requirements.

## Validation coverage

| Check | Result and scope |
| --- | --- |
| Root module browser tests and nested e2e module | Passed with race detection, including the sequential `scripts/check.sh browser` run. |
| Application/browser failure matrix | Passed as both unprivileged user and root. Covered application `SIGKILL` during startup and after browser use, panic, `os.Exit`, timeout-like panic, normal close, and browser `SIGKILL` while its owner stayed alive. |
| Guardian regressions | Passed with race detection, including an actual Go test timeout, descendant cleanup, and temporary-path confinement. |
| Resource audit | Successful audited cases ended with **zero remaining tracked processes (including zombies), generated profiles, browser scratch files, or new listening endpoints**. No successful case needed external process cleanup. |
| Sequential execution and cached tester | Verified sequential scheduling, browser reuse, retirement after an intentional test failure, and replacement with a fresh browser/profile. |
| Cleanup benchmark | Serial browser lifecycle cleanup passed with race detection. |
| Non-Linux compilation | `lib/launcher` compiled for `darwin/amd64`, `freebsd/amd64`, and `windows/amd64` using Go 1.27.1 and `CGO_ENABLED=0`. Target binaries were **not executed**. |

The recorded tests did not cover other Chrome versions, Chromium, Edge,
headful/display-server operation, Docker images, or other OS/CPU combinations.

## Platform requirements and known gaps

- **Linux:** supervision requires readable `/proc/self/task/*/children` and permission to enable `PR_SET_CHILD_SUBREAPER`. Managed profile cleanup also needs `/proc/self/fd`. Restricted containers or system policies can prevent launch even on a tested distribution.
- **Other Unix systems:** supervision kills the browser process group; detached descendants can survive. Managed profile cleanup additionally requires reopening the inherited directory through `/dev/fd`, which has not been validated on these systems.
- **Windows:** the launcher starts Chrome directly and force-kills the initial process. There is no application-death guardian or Windows Job Object containing the process tree. Normal automation may work, but the Linux failure-cleanup result does not apply.
- **Test tooling:** `scripts/check.sh` requires Bash and [race-detector support](https://go.dev/src/internal/platform/supported.go) for the target OS and CPU.

Implementation details, cleanup usage, and limitations are documented in the
[launcher guide](../lib/launcher/README.md). The platform assessments are based
on [browser discovery](../lib/launcher/browser.go), the
[Linux guardian](../lib/launcher/guardian_linux.go),
[other Unix cleanup](../lib/launcher/guardian_other_unix.go),
[Windows launch path](../lib/launcher/guardian_windows.go), and
[Unix process setup](../lib/launcher/os_unix.go).

Connecting through `Browser.ControlURL` delegates browser provisioning and
process cleanup to the browser host. It can avoid local browser availability
requirements, but does not qualify an untested client platform.

For test commands, see [Development](../README.md#development).
