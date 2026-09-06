#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
export GODEBUG="${GODEBUG:+$GODEBUG,}tracebackancestors=100"
modules=(. lib/docker lib/examples/custom-websocket lib/examples/e2e-testing)
# Keep package test binaries and test cases sequential to bound browser resources.
test_flags=(-mod=readonly -count=1 -race -cover -covermode=atomic -p=1 -parallel=1)

case "${1:-pure}" in
pure)
    sources=()
    while IFS= read -r -d '' source; do
        if [[ -f "$source" ]]; then sources+=("$source"); fi
    done < <(git ls-files --cached --others --exclude-standard -z '*.go')
    unformatted=$(gofmt -l "${sources[@]}")
    if [[ -n "$unformatted" ]]; then
        printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
        exit 1
    fi
    git diff --check

    # Root ./... excludes nested modules; inspect each with both resolvers.
    for module in "${modules[@]}"; do
        (
            cd "$module"
            go vet -mod=readonly ./...
            GOWORK=off go vet -mod=readonly ./...
            # Compile test binaries without running package initialization.
            go test -mod=readonly -exec=true ./...
            GOWORK=off go test -mod=readonly -exec=true ./...
        )
    done

    pure_packages=(./internal/... ./lib/assets/... ./lib/defaults/... ./lib/devices/...
        ./lib/input/... ./lib/js/... ./lib/jsonvalue/... ./lib/proto/... ./lib/utils/...
        ./lib/launcher/rod-manager/...)
    go test "${test_flags[@]}" "${pure_packages[@]}"
    GOWORK=off go test "${test_flags[@]}" "${pure_packages[@]}"
    go test "${test_flags[@]}" -run '^Test(WebSocket(HandshakeLifecycle|EstablishmentContext|TLSCancellation|Err|Header)|Client(MarshalError|MalformedMessage|ResponseBeforeEOF|PendingResponseRouting)|SlowSend|CancelCallLeak|ConcurrentCall|Format|ContextDestroyedErrors)$' ./lib/cdp
    go test "${test_flags[@]}" -run '^Test(ResolveURL.*|TestOpen|CleanupWithoutProcess|CleanupReusedBrowser|URLParserBoundedOutput|GuardianCleansDescendants|GuardianParentExit|GuardianManagedProfileCleanup|GuardianScratchCleanupOnStartupFailure|GuardianTemporaryCleanupConfined|GuardianTemporaryDirectoryResolvesSymlinks|ManagerLaunchStopsWhenRequestIsCanceled)$' ./lib/launcher
    go test "${test_flags[@]}" -run '^Test(LongestCommonSubsequence|SaveFileDefaultPaths|Typed|ShapesEqual|OwnedBrowserClose.*|AttachedBrowserCloseContext|MonitorCancellation)' .
    ;;
fix)
    # Review advisory suggestions for callback panic behavior and protocol encoding.
    report_dir=$(mktemp -d)
    trap 'rm -rf "$report_dir"' EXIT
    report="$report_dir/fix.diff"
    diagnostics="$report_dir/fix.stderr"
    for module in "${modules[@]}"; do
        printf 'go fix suggestions for %s\n' "$module"
        if (cd "$module" && go fix -diff -mod=readonly ./...) >"$report" 2>"$diagnostics"; then
            cat "$report"
            cat "$diagnostics" >&2
        else
            status=$?
            cat "$report"
            cat "$diagnostics" >&2
            # Suggested patches use ---/+++ headers. A failed analysis is an error.
            if [[ "$status" != 1 || -s "$diagnostics" ]] || ! grep -q '^--- ' "$report"; then
                exit 1
            fi
        fi
    done
    ;;
browser)
    go test "${test_flags[@]}" -run '^Test' ./...
    go test "${test_flags[@]}" ./lib/examples/e2e-testing/...
    ;;
live)
    go test "${test_flags[@]}" -run '^Example' .
    ;;
docker)
    go test "${test_flags[@]}" ./lib/docker/...
    ;;
*)
    printf 'usage: %s [pure|fix|browser|live|docker]\n' "$0" >&2
    exit 2
    ;;
esac
