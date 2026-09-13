#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
export GODEBUG="${GODEBUG:+$GODEBUG,}tracebackancestors=100"
modules=(. lib/docker examples/custom-websocket examples/e2e-testing)
# Keep package test binaries and test cases sequential to bound browser resources.
test_flags=(-mod=readonly -count=1 -race -cover -covermode=atomic -p=1 -parallel=1)

# Join related test-name patterns while keeping package selections readable.
test_pattern() {
    local IFS='|'
    printf '^Test(%s)$' "$*"
}

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
    # These mixed packages also contain browser tests. Select deterministic
    # protocol, local HTTP, and helper-process fixtures explicitly.
    cdp_tests=(
        'WebSocket(HandshakeLifecycle|EstablishmentContext|TLSCancellation|Err|Header)'
        'WebSocket(SendContext|CanceledSendPreservesTransport|ControlReplyTimeout)'
        'WebSocketProtocol(Handshake|Frames|Close|MalformedFrames)'
        'Client(MarshalError|MalformedMessage|ResponseBeforeEOF|PendingResponseRouting)'
        'Client(CloseUnreadEvent|WebSocketPeerClose)'
        'SlowSend|CancelCallLeak|ConcurrentCall|Format|ContextDestroyedErrors'
    )
    launcher_tests=(
        'ResolveURL.*|TestOpen'
        'Cleanup(WithoutProcess|ReusedBrowser|ContextBudget)'
        'OutputTail|OwnedProfileCollision|URLParserBoundedOutput'
        'ManagedInitializationCancellation|FormatArgsPreservesProfileOwnership|FreeBSDDiscovery'
        'Guardian(CleansDescendants|ParentExit|ManagedProfileCleanup|ScratchCleanupOnStartupFailure)'
        'Guardian(TemporaryCleanupConfined|TemporaryDirectoryResolvesSymlinks)'
        'ManagerLaunchStopsWhenRequestIsCanceled|ManagerAuthentication'
    )
    rod_tests=(
        'LongestCommonSubsequence|SaveFileDefaultPaths|ShapesEqual|Typed.*'
        'OwnedBrowserClose(ExpiredContext|Timeout)|AttachedBrowserCloseContext|MonitorCancellation'
        'ConfiguredBrowser(LaunchFailures|LaunchConflicts|CleanupBudget|OutputLimit|DiscoveryFailureClosesTransport)'
        'Diagnostics(Lifecycle|SetupFailure|IncompleteStop|SharedDomains|CorrelationAndRevocation)'
        'Diagnostics(RetentionAndConcurrentSnapshots|ValueFormatting|DomainTransitionCancellation|ExceptionPreservesStackURL)'
        'Shared(DomainProtocolScope|LifecycleSetting)|EventWaitReportsSetupAndCancellation'
        'Domain(SetupAndRestoreErrors|DisableRestoresConfiguration|RestoreBounded)|SetExtraHeadersDomainErrors'
        'BrowserDisconnectClosesUnreadEvent|SessionLateReplyDoesNotRestoreState'
        'Page(SessionViewsAndEviction|AttachLockCancellation|CloseRequiresClosureEvidence|WaitNavigationIgnoresChildFrames)'
        'PageCloseAcknowledgementAfterSessionTermination'
        'PageSessionEndClosesUnreadEvent'
        'KeyboardStateCommitsAfterSuccess|KeyActionsFailureReleasesOwnedKeys'
        'Download(ContextAndGUID|UnusedCancellationRestoresBehavior|SetupFailureRollsBack)'
        'Download(BrowserCancellation|DistinctContexts|DisposedContextIsNotDefault)'
        'Download(SharedEventsRestoration|FailedSetupPreservesSharedEvents)'
        'Page(ReloadWaitErrors|HandleDialogWaitErrors|HandleFileDialogLifecycle)'
        'StreamReader(FinalData|DrainsBufferedDataBeforeError|AdvancesExplicitOffset)'
        'ElementEqualReturnsEvaluationError'
        'JSHelperCache(InvalidationAcrossViews|UnsetInvalidatesSharedContext)'
        'FrameContextErrorPreservesCause'
        'Hijack(RepeatedHeaders|ReplayableBodies|RouteMutation|RouteDispatch|LifecycleErrors)'
        'HandleAuthErrorsAndRestore'
    )
    go test "${test_flags[@]}" -run "$(test_pattern "${cdp_tests[@]}")" ./lib/cdp
    go test "${test_flags[@]}" -run "$(test_pattern "${launcher_tests[@]}")" ./lib/launcher
    go test "${test_flags[@]}" -run "$(test_pattern "${rod_tests[@]}")" .
    go test "${test_flags[@]}" -run '^Test(HTMLHandler|InvalidConfiguration|SetupRollback)$' ./lib/fixture
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
examples)
    go test "${test_flags[@]}" ./examples/...
    for module in examples/custom-websocket examples/e2e-testing; do
        (cd "$module" && go test "${test_flags[@]}" ./...)
    done
    ;;
browser)
    # Report protocol drift without skipping runtime compatibility checks.
    protocol_flags=(-check)
    if [[ ${ROD_PROTOCOL_NO_SANDBOX:-0} == 1 ]]; then
        protocol_flags+=(-no-sandbox)
    fi
    protocol_status=0
    go run -mod=readonly ./lib/proto/generate "${protocol_flags[@]}" || protocol_status=$?
    go test "${test_flags[@]}" -run '^Test' ./...
    for module in examples/custom-websocket examples/e2e-testing; do
        (cd "$module" && go test "${test_flags[@]}" ./...)
    done
    if (( protocol_status != 0 )); then exit "$protocol_status"; fi
    ;;
live)
    go test "${test_flags[@]}" -run '^Example' .
    ;;
docker)
    go test "${test_flags[@]}" ./lib/docker/...
    ;;
*)
    printf 'usage: %s [pure|fix|examples|browser|live|docker]\n' "$0" >&2
    exit 2
    ;;
esac
