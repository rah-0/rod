# Protocol changes compared with v0.120.0

The generated `lib/proto` API uses the protocol reported by Chrome
`152.0.7977.64`, replacing the Chrome `128.0.6568.0` schema used in v0.120.0.
The browser version identifies the generation source; it does not select the
browser used by applications or tests. See [protocol generation](../lib/proto/generate/README.md)
for capture, regeneration, and checks against the installed browser.

All Go identifiers below belong to `proto`. Removed command types also lose
`Call` and `ProtoReq`; removed event types also lose `ProtoEvent`. These APIs
are absent from the captured protocol. Remove their callers and subscriptions;
a retained raw method name or string constant does not restore browser support.

Use keyed struct literals when constructing protocol values. Fields are added,
removed, and reordered, so positional literals require migration even where
individual surviving fields retain their types. Ordinary Go builds continue to
use the committed declarations and do not regenerate them automatically.

## Changed fields and wire encoding

| Field | Change | Migration |
| --- | --- | --- |
| `AnimationAnimationEffect.Iterations` | `float64` becomes `*float64`; `iterations` gains `omitempty`. | Check for nil when reading. Use `new(float64(value))` to supply a value and nil for absence. |
| `WebAuthnCredential.SignCount` | `int` becomes `*int`; `signCount` gains `omitempty`. | Check for nil when reading. Use `new(value)` to set an integer, including zero. |
| `DebuggerScriptParsed.DebugSymbols` | `*DebuggerDebugSymbols` becomes `[]*DebuggerDebugSymbols`. | Iterate the entries; use an empty/nil slice when no symbols are provided. |
| `AuditsAffectedRequest.RequestID` | `requestId` gains `omitempty`; its Go type is unchanged. | Accept missing request IDs; an empty ID is omitted when marshaling. |
| `AuditsAffectedRequest.URL` | `url` loses `omitempty` and is required by the schema. | Populate the URL in constructed values and fixtures; it is serialized even when empty. |
| `AutofillTrigger.Card` | `card` gains `omitempty`; its pointer type is unchanged. | Leave nil to omit a card, including when supplying the new `Address` field. |
| `IndexedDBRequestData.IndexName` | `indexName` gains `omitempty`. | Leave empty to omit the index name and request object-store data. |
| `NetworkSetBlockedURLs.Urls` | `urls` gains `omitzero`. | A nil slice omits the field, allowing `URLPatterns`-only requests. Use an empty non-nil slice to send `[]` and clear URL blocking. |
| `PageSetRPHRegistrationMode.Mode` | `PageAutoResponseMode` becomes `PageSetRPHRegistrationModeMode`. | Use the command-specific `None`, `AutoAccept`, or `AutoReject` constants. RPH no longer exposes an `AutoOptOut` value. |
| `PageSetSPCTransactionMode.Mode` | `PageAutoResponseMode` becomes `PageSetSPCTransactionModeMode`. | Replace the old constants with the corresponding command-specific constants; `AutoChooseToAuthAnotherWay` is also available. |
| `NetworkClientSecurityState.PrivateNetworkRequestPolicy` | Removed; the required field is now `LocalNetworkAccessRequestPolicy`, typed `NetworkLocalNetworkAccessRequestPolicy`. | Read and construct the new field. Revisit policy handling: the new enum has permission-based values rather than the old preflight values. |
| `StorageSharedStorageAccessed.Type` | Removed; `Scope`, `Method`, and `OwnerSite` are required. | Classify access using the separate scope and method enums and retain the owner site when needed. |
| `NetworkCookie.SameParty`, `NetworkCookieParam.SameParty`, `NetworkSetCookie.SameParty` | Removed. `CookiesToParams` no longer copies this attribute. | Remove the field from literals and cookie-processing code. |
| `AuditsInspectorIssueDetails.LowTextContrastIssueDetails`, `AuditsInspectorIssueDetails.AttributionReportingIssueDetails` | Removed with their detail types. | Remove handling and fixtures for these issue payloads. |
| `CSSGetMatchedStylesForNodeResult.CSSPositionFallbackRules` | Removed with `CSSCSSPositionFallbackRule`. | Inspect `CSSPositionTryRules` and `ActivePositionFallbackIndex` using their current schemas; they are not the old nested fallback-rule representation. |
| `CSSGetMatchedStylesForNodeResult.CSSFontPaletteValuesRule` | Removed with `CSSCSSFontPaletteValuesRule`. | Inspect `CSSAtRules` entries with type `CSSCSSAtRuleTypeFontPaletteValues`; update code for the new representation. |
| `SystemInfoGPUInfo.ImageDecoding` | Removed with `SystemInfoImageDecodeAcceleratorCapability`. | Stop expecting image-decoder capability records in GPU information. |

### Stylesheet identifiers

`CSSStyleSheetID` is removed. Use `DOMStyleSheetID` for stylesheet identifiers,
including variable declarations, function parameters, maps, and conversions
from strings. The `StyleSheetID` field changes to that type on every following
struct; each field retains its JSON name and optionality:

- `CSSAddRule.StyleSheetID`
- `CSSCSSContainerQuery.StyleSheetID`
- `CSSCSSKeyframeRule.StyleSheetID`
- `CSSCSSLayer.StyleSheetID`
- `CSSCSSMedia.StyleSheetID`
- `CSSCSSPositionTryRule.StyleSheetID`
- `CSSCSSPropertyRule.StyleSheetID`
- `CSSCSSRule.StyleSheetID`
- `CSSCSSScope.StyleSheetID`
- `CSSCSSStyle.StyleSheetID`
- `CSSCSSStyleSheetHeader.StyleSheetID`
- `CSSCSSSupports.StyleSheetID`
- `CSSCSSTryRule.StyleSheetID`
- `CSSCollectClassNames.StyleSheetID`
- `CSSCreateStyleSheetResult.StyleSheetID`
- `CSSGetLocationForSelector.StyleSheetID`
- `CSSGetStyleSheetText.StyleSheetID`
- `CSSRuleUsage.StyleSheetID`
- `CSSSetContainerQueryText.StyleSheetID`
- `CSSSetKeyframeKey.StyleSheetID`
- `CSSSetMediaText.StyleSheetID`
- `CSSSetPropertyRulePropertyName.StyleSheetID`
- `CSSSetRuleSelector.StyleSheetID`
- `CSSSetScopeText.StyleSheetID`
- `CSSSetStyleSheetText.StyleSheetID`
- `CSSSetSupportsText.StyleSheetID`
- `CSSStyleDeclarationEdit.StyleSheetID`
- `CSSStyleSheetChanged.StyleSheetID`
- `CSSStyleSheetRemoved.StyleSheetID`

### Newly required fields on existing types

The following fields are required in the captured response/event or value
schema. Update constructed protocol values and mock payloads to include them;
`encoding/json` itself does not validate missing required response fields.
Read the additional values where they affect interpretation.

| Field | Go type | Migration |
| --- | --- | --- |
| `CSSCSSContainerQuery.ConditionText` | `string` | Preserve the query condition text in constructed CSS query values. |
| `CSSCSSPositionTryRule.Active` | `bool` | Use this flag to identify an active position-try rule. |
| `CSSGetComputedStyleForNodeResult.ExtraFields` | `*CSSComputedStyleExtraFields` | Include the extra-fields object in complete computed-style fixtures. |
| `DebuggerScriptFailedToParse.BuildID` | `string` | Preserve the browser-provided build identifier, which may be empty. |
| `DebuggerScriptParsed.BuildID` | `string` | Preserve the browser-provided build identifier, which may be empty. |
| `NetworkClientSecurityState.LocalNetworkAccessRequestPolicy` | `NetworkLocalNetworkAccessRequestPolicy` | Use the replacement policy field and enum described above. |
| `NetworkGetRequestPostDataResult.Base64Encoded` | `bool` | Decode `PostData` with standard base64 when true; otherwise use the text directly. |
| `NetworkSignedExchangeInfo.HasExtraInfo` | `bool` | Use the flag when correlating signed-exchange extra information. |
| `PageJavascriptDialogClosed.FrameID` | `PageFrameID` | Retain the frame identity when handling or constructing dialog events. |
| `PageJavascriptDialogOpening.FrameID` | `PageFrameID` | Retain the frame identity when handling or constructing dialog events. |
| `PageNavigatedWithinDocument.NavigationType` | `PageNavigatedWithinDocumentNavigationType` | Handle the navigation type reported by the event. |
| `PreloadPrefetchStatusUpdated.PipelineID` | `PreloadPreloadPipelineID` | Use the pipeline identifier when correlating preloading status. |
| `PreloadPrerenderStatusUpdated.PipelineID` | `PreloadPreloadPipelineID` | Use the pipeline identifier when correlating preloading status. |
| `RuntimeGetHeapUsageResult.EmbedderHeapUsedSize` | `float64` | Include embedder heap usage in result fixtures and memory reporting as needed. |
| `RuntimeGetHeapUsageResult.BackingStorageSize` | `float64` | Include backing storage usage in result fixtures and memory reporting as needed. |
| `StorageSharedStorageAccessed.Scope` | `StorageSharedStorageAccessScope` | Use the new scope enum instead of parsing the removed combined `Type`. |
| `StorageSharedStorageAccessed.Method` | `StorageSharedStorageAccessMethod` | Use the new method enum instead of parsing the removed combined `Type`. |
| `StorageSharedStorageAccessed.OwnerSite` | `string` | Preserve the site separately from the existing owner origin. |

## Removed types

The groups below list every removed exported type, including request and result
types. The [removed constants](#removed-enum-constants) section covers enum
constants, including those belonging to removed types.

### Audits

Remove contrast-check commands and the removed audit payload handlers. The protocol no longer supplies these types.

- `AuditsAttributionReportingIssueDetails`
- `AuditsAttributionReportingIssueType`
- `AuditsCheckContrast`
- `AuditsLowTextContrastIssueDetails`

### CSS

Apply the stylesheet-ID and CSS-rule representation migrations above.

- `CSSCSSFontPaletteValuesRule`
- `CSSCSSPositionFallbackRule`
- `CSSStyleSheetID`

### Database

Remove calls and event handlers for the Database domain. The captured protocol has no Database domain; the refresh does not provide a replacement database API.

- `DatabaseAddDatabase`
- `DatabaseDatabase`
- `DatabaseDatabaseID`
- `DatabaseDisable`
- `DatabaseEnable`
- `DatabaseError`
- `DatabaseExecuteSQL`
- `DatabaseExecuteSQLResult`
- `DatabaseGetDatabaseTableNames`
- `DatabaseGetDatabaseTableNamesResult`

### Media

Subscribe to `MediaPlayerCreated` (`Media.playerCreated`) and read its `Player` object. It reports one player per event rather than a list of player IDs.

- `MediaPlayersCreated`

### Network

Use the local-network policy migration above. Remove subscriptions to the four removed subresource Web Bundle events; this refresh does not provide equivalent events.

- `NetworkPrivateNetworkRequestPolicy`
- `NetworkSubresourceWebBundleInnerResponseError`
- `NetworkSubresourceWebBundleInnerResponseParsed`
- `NetworkSubresourceWebBundleMetadataError`
- `NetworkSubresourceWebBundleMetadataReceived`

### Page

Use command-specific response-mode enums as described above. Remove `PageGetAdScriptID` calls and dependencies on its result and identifier types.

- `PageAdScriptID`
- `PageAutoResponseMode`
- `PageGetAdScriptID`
- `PageGetAdScriptIDResult`

### ServiceWorker

Remove `ServiceWorkerInspectWorker` calls. The captured protocol no longer exposes this inspection command.

- `ServiceWorkerInspectWorker`

### Storage attribution reporting

Remove the listed attribution-reporting commands, event subscriptions, and data models. These interfaces are absent from the captured schema.

- `StorageAttributionReportingAggregatableDedupKey`
- `StorageAttributionReportingAggregatableResult`
- `StorageAttributionReportingAggregatableTriggerData`
- `StorageAttributionReportingAggregatableValueDictEntry`
- `StorageAttributionReportingAggregatableValueEntry`
- `StorageAttributionReportingAggregationKeysEntry`
- `StorageAttributionReportingEventLevelResult`
- `StorageAttributionReportingEventReportWindows`
- `StorageAttributionReportingEventTriggerData`
- `StorageAttributionReportingFilterConfig`
- `StorageAttributionReportingFilterDataEntry`
- `StorageAttributionReportingFilterPair`
- `StorageAttributionReportingSourceRegistered`
- `StorageAttributionReportingSourceRegistration`
- `StorageAttributionReportingSourceRegistrationResult`
- `StorageAttributionReportingSourceRegistrationTimeConfig`
- `StorageAttributionReportingSourceType`
- `StorageAttributionReportingTriggerDataMatching`
- `StorageAttributionReportingTriggerRegistered`
- `StorageAttributionReportingTriggerRegistration`
- `StorageAttributionReportingTriggerSpec`
- `StorageSendPendingAttributionReports`
- `StorageSendPendingAttributionReportsResult`
- `StorageSetAttributionReportingLocalTestingMode`
- `StorageSetAttributionReportingTracking`

### Storage interest groups

Remove interest-group commands, tracking subscriptions, and dependent data models. These interfaces are absent from the captured schema.

- `StorageGetInterestGroupDetails`
- `StorageGetInterestGroupDetailsResult`
- `StorageInterestGroupAccessType`
- `StorageInterestGroupAccessed`
- `StorageInterestGroupAuctionEventOccurred`
- `StorageInterestGroupAuctionEventType`
- `StorageInterestGroupAuctionFetchType`
- `StorageInterestGroupAuctionID`
- `StorageInterestGroupAuctionNetworkRequestCreated`
- `StorageSetInterestGroupAuctionTracking`
- `StorageSetInterestGroupTracking`

### Other Storage types

Replace `StorageSharedStorageAccessType` with separate scope/method handling. Replace application-owned uses of the removed numeric-string wrappers with your own types; their associated protocol interfaces are removed.

- `StorageSharedStorageAccessType`
- `StorageSignedInt64AsBase10`
- `StorageUnsignedInt128AsBase16`
- `StorageUnsignedInt64AsBase10`

### SystemInfo

Remove code expecting `SystemInfoGPUInfo.ImageDecoding` or its capability element type.

- `SystemInfoImageDecodeAcceleratorCapability`

## Removed enum constants

Each removed Go identifier is the **type prefix concatenated with a suffix** in
the following table. For example, `DebuggerDebugSymbolsType` plus `None` means
`DebuggerDebugSymbolsTypeNone`. These are the exact 169 removed constants.

Remove obsolete switch branches, fixtures, and outgoing values. For enums whose
types remain, handle the current values and retain an unknown-value path when
processing browser events. For removed enum types, follow their replacement or
removal guidance above. Do not assume an old value has an equivalent replacement.

`NetworkIPAddressSpace` now contains `Loopback`, `Local`, `Public`, and `Unknown`;
review address-space classification instead of retaining the removed `Private`
constant. For response modes, use the command-specific constants described above.
For debugger symbols, an absent or empty list replaces reliance on a `None` entry.

| Type prefix | Removed suffixes |
| --- | --- |
| `AuditsAttributionReportingIssueType` | `InsecureContext`, `InvalidHeader`, `InvalidInfoHeader`, `InvalidRegisterOsSourceHeader`, `InvalidRegisterOsTriggerHeader`, `InvalidRegisterTriggerHeader`, `NavigationRegistrationWithoutTransientUserActivation`, `NoRegisterOsSourceHeader`, `NoRegisterOsTriggerHeader`, `NoRegisterSourceHeader`, `NoRegisterTriggerHeader`, `NoWebOrOsSupport`, `OsSourceIgnored`, `OsTriggerIgnored`, `PermissionPolicyDisabled`, `SourceAndTriggerHeaders`, `SourceIgnored`, `TriggerIgnored`, `UntrustworthyReportingOrigin`, `WebAndOsHeaders` |
| `AuditsCookieExclusionReason` | `ExcludeInvalidSameParty`, `ExcludeSamePartyCrossPartyContext` |
| `AuditsFederatedAuthRequestIssueReason` | `ClientMetadataHTTPNotFound`, `ClientMetadataInvalidContentType`, `ClientMetadataInvalidResponse`, `ClientMetadataNoResponse`, `InvalidFieldsSpecified`, `ReplacedByButtonMode`, `ThirdPartyCookiesBlocked` |
| `AuditsGenericIssueErrorType` | `CrossOriginPortalPostMessageError`, `FormAriaLabelledByToNonExistingID`, `FormLabelHasNeitherForNorNestedInput` |
| `AuditsInspectorIssueCode` | `AttributionReportingIssue`, `LowTextContrastIssue` |
| `AuditsMixedContentResourceType` | `AttributionSrc` |
| `BrowserPermissionType` | `AccessibilityEvents`, `Flash`, `VideoCapturePanTiltZoom` |
| `DebuggerDebugSymbolsType` | `None` |
| `EmulationSensorType` | `Proximity` |
| `NetworkCookieBlockedReason` | `SamePartyFromCrossPartyContext` |
| `NetworkCookieExemptionReason` | `CorsOptIn`, `TPCDDeprecationTrial`, `TPCDHeuristics`, `TPCDMetadata` |
| `NetworkCorsError` | `InsecurePrivateNetwork`, `InvalidPrivateNetworkAccess`, `PreflightInvalidAllowPrivateNetwork`, `PreflightMissingAllowPrivateNetwork`, `PreflightMissingPrivateNetworkAccessID`, `PreflightMissingPrivateNetworkAccessName`, `PrivateNetworkAccessPermissionDenied`, `PrivateNetworkAccessPermissionUnavailable`, `UnexpectedPrivateNetworkAccess` |
| `NetworkIPAddressSpace` | `Private` |
| `NetworkPrivateNetworkRequestPolicy` | `Allow`, `BlockFromInsecureToMorePrivate`, `PreflightBlock`, `PreflightWarn`, `WarnFromInsecureToMorePrivate` |
| `NetworkSetCookieBlockedReason` | `SamePartyConflictsWithOtherAttributes`, `SamePartyFromCrossPartyContext` |
| `OverlayInspectMode` | `ShowDistances` |
| `PageAutoResponseMode` | `AutoAccept`, `AutoOptOut`, `AutoReject`, `None` |
| `PageBackForwardCacheNotRestoredReason` | `Portal`, `WebRTCSticky`, `WebSocketSticky`, `WebTransportSticky` |
| `PagePermissionsPolicyFeature` | `AttributionReporting`, `SharedAutofill` |
| `PreloadPrefetchStatus` | `PrefetchFailedPerPageLimitExceeded` |
| `PreloadPrerenderFinalStatus` | `MainFrameNavigation` |
| `StorageAttributionReportingAggregatableResult` | `Deduplicated`, `ExcessiveAttributions`, `ExcessiveReportingOrigins`, `ExcessiveReports`, `InsufficientBudget`, `InternalError`, `NoCapacityForAttributionDestination`, `NoHistograms`, `NoMatchingSourceFilterData`, `NoMatchingSources`, `NotRegistered`, `ProhibitedByBrowserPolicy`, `ReportWindowPassed`, `Success` |
| `StorageAttributionReportingEventLevelResult` | `Deduplicated`, `ExcessiveAttributions`, `ExcessiveReportingOrigins`, `ExcessiveReports`, `FalselyAttributedSource`, `InternalError`, `NeverAttributedSource`, `NoCapacityForAttributionDestination`, `NoMatchingConfigurations`, `NoMatchingSourceFilterData`, `NoMatchingSources`, `NoMatchingTriggerData`, `NotRegistered`, `PriorityTooLow`, `ProhibitedByBrowserPolicy`, `ReportWindowNotStarted`, `ReportWindowPassed`, `Success`, `SuccessDroppedLowerPriority` |
| `StorageAttributionReportingSourceRegistrationResult` | `DestinationBothLimitsReached`, `DestinationGlobalLimitReached`, `DestinationPerDayReportingLimitReached`, `DestinationReportingLimitReached`, `ExceedsMaxChannelCapacity`, `ExceedsMaxTriggerStateCardinality`, `ExcessiveReportingOrigins`, `InsufficientSourceCapacity`, `InsufficientUniqueDestinationCapacity`, `InternalError`, `ProhibitedByBrowserPolicy`, `ReportingOriginsPerSiteLimitReached`, `Success`, `SuccessNoised` |
| `StorageAttributionReportingSourceRegistrationTimeConfig` | `Exclude`, `Include` |
| `StorageAttributionReportingSourceType` | `Event`, `Navigation` |
| `StorageAttributionReportingTriggerDataMatching` | `Exact`, `Modulus` |
| `StorageInterestGroupAccessType` | `AdditionalBid`, `AdditionalBidWin`, `Bid`, `Clear`, `Join`, `Leave`, `Loaded`, `TopLevelAdditionalBid`, `TopLevelBid`, `Update`, `Win` |
| `StorageInterestGroupAuctionEventType` | `ConfigResolved`, `Started` |
| `StorageInterestGroupAuctionFetchType` | `BidderJs`, `BidderTrustedSignals`, `BidderWasm`, `SellerJs`, `SellerTrustedSignals` |
| `StorageSharedStorageAccessType` | `DocumentAddModule`, `DocumentAppend`, `DocumentClear`, `DocumentDelete`, `DocumentGet`, `DocumentRun`, `DocumentSelectURL`, `DocumentSet`, `HeaderAppend`, `HeaderClear`, `HeaderDelete`, `HeaderSet`, `WorkletAppend`, `WorkletClear`, `WorkletDelete`, `WorkletEntries`, `WorkletGet`, `WorkletKeys`, `WorkletLength`, `WorkletRemainingBudget`, `WorkletSet` |
| `StorageStorageType` | `Appcache`, `InterestGroups` |
