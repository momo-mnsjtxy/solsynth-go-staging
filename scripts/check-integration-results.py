#!/usr/bin/env python3
"""Fail closed unless each required real PostgreSQL test passed, never skipped."""
import json
import sys

EXPECTED = {
    "before": {
        "TestPGLiveStreamChatHTTPHistoryAndIdempotency",
        "TestPGLiveStreamChatTimeoutDeleteAndExpiry",
        "TestPGLiveStreamChatOutboxRetryRecipientsAndRecordedRevocation",
        "TestPGLiveStreamChatRollbackPacketBoundsAndGeneration",
        "TestPGLiveStreamChatVisibilityAndDisableDuringLockWait",
        "TestPGLiveStreamChatRetryLimitExpiryAndUnconfigured",
        "TestPGLiveStreamChatOutboxRestartPersistence",
        "TestPGLiveStreamChatProviderAcceptanceRollbackAndFIFO",
        "TestPGLiveStreamChatOpposingModeratorsAndParentRevocation",
        "TestPGBackendLivestreamChatComposition",
        "TestPGBackendFederatedRenameComposition",

        "TestPGFederatedRenameStableCachedURLsAndCardReplay",
        "TestPGFederatedRenameExplicitOriginInitialization",
        "TestPGFederatedRenameClaimsCapAndRetainedOwnership",
        "TestPGFederatedRenameQueuedDeliveryAndCurrentPrivacy",
        "TestPGFederatedRenameRollbackAndPrivilegedSharedPolicy",
        "TestPGFederatedRenameRestartPersistence",
        "TestPGAdminVerificationLifecycleAndPrivacy",
        "TestPGAdminVerificationAuthorityAndRollback",
        "TestPGAdminVerificationCurrentTargetAfterWait",
        "TestPGAdminStatisticsCanonicalCounts",
        "TestPGAdminStatisticsPermissionAndCurrentProof",
        "TestPGAutomodRulesLifecycleAndExplicitEvaluation",
        "TestPGAutomodBulkAtomicityAndConcurrency",
        "TestPGAutomodRuleMutationRevocation",
        "TestPGAdminVerificationNestedPublisherProjection",
        "TestPGAdminExtensionsRestartPersistence",
        "TestPGBackendAdminExtensionsComposition",

        "TestPGLiveStreamLifecycleTokensAndPrivateVisibility",
        "TestPGLiveStreamIngressUncertainCreateAndDurableCleanup",
        "TestPGLiveStreamCapturedPermissionAndPublisherRevocation",
        "TestPGLiveStreamUnknownIngressOutcomeFailsClosed",
        "TestPGLiveStreamConcurrentIdempotencyAndProviderRollback",
        "TestPGLiveStreamCanonicalThumbnailAndUnconfiguredProvider",
        "TestPGLiveStreamDisableDuringLockWait",
        "TestPGLiveStreamParentExpiryAndIdleCleanup",
        "TestPGLiveStreamCleanupRestartPersistence",
        "TestPGBackendLivestreamComposition",

        "TestPGContentModerationGlobalIsolationAndLifecycle",
        "TestPGContentModerationShadowbanPropagation",
        "TestPGContentModerationAtomicBatchAndAuditRollback",
        "TestPGContentModerationScopedRemovalAndNotification",
        "TestPGContentModerationOwnerTransferAndDeletedRealm",
        "TestPGContentModerationCurrentAuthorityAndMembershipAfterWait",
        "TestPGContentModerationFederationWithdrawalRetry",
        "TestPGContentModerationRestartPersistence",
        "TestPGContentModerationConcurrentBatchesAndScopedRevocation",
        "TestPGContentModerationPublicMetadataAndNestedReads",
        "TestPGBackendContentModerationComposition",

        "TestPGNameCardPaidAccountRenameAndIdempotency",
        "TestPGNameCardPaymentAndRenameRollback",
        "TestPGNameCardOneUseAndFreshPublisherAuthority",
        "TestPGNameCardFederationBoundaryAndSourceFreeRealmSlug",
        "TestPGNameCardRestartPersistence",
        "TestPGRewindCanonicalSnapshotSharingAndRevocation",
        "TestPGRewindConfiguredComponentsAndIsolation",
        "TestPGRewindAtomicCreationAndBlockedPublicRevocation",
        "TestPGRewindRestartPersistence",
        "TestPGBackendPaidNameCardComposition",
        "TestPGBackendRewindComposition",

        "TestPGBackendIPCheckComposition",
        "TestPGBackendAccountSettingsComposition",
        "TestPGProfileAuthoredAliasesAndPrivateBirthday",
        "TestPGContactVerificationPrimaryAndPublicLifecycle",
        "TestPGContactEmailOwnershipAndRegistrationRace",
        "TestPGContactAtomicityExpiryAndAuthority",
        "TestPGNotificationPreferenceProjectionAndQueuedDelivery",
        "TestPGAccountSettingsRestartPersistence",

        "TestPGBackendTranslationComposition",
        "TestPGBackendAutocompleteComposition",
        "TestPGAutocompleteCanonicalVisibilityAndPreferences",
        "TestPGAutocompleteStickerVisibilityAndMediaChanges",
        "TestPGAutocompleteFreshProofAndBoundedQueries",
        "TestPGAutocompleteStickerRealmLockOrderingAndRecheck",
        "TestPGAutocompleteChatRelationAndRealmPrivacy",
        "TestPGAutocompleteRestartPersistence",
        "TestPGTranslationEntitlementCacheAndIsolation",
        "TestPGTranslationRejectsIneligibleCanonicalGrants",
        "TestPGTranslationProviderWaitRechecksAuthorityAndEntitlement",
        "TestPGTranslationCacheCapacityRollbackAndConcurrency",
        "TestPGTranslationFreshProofWhileAccountLocked",
        "TestPGTranslationRestartPersistence",

        "TestPGChatAuthorityPreflightSameTransactionAndNoLocks",
        "TestPGBackendChatRelationsComposition",
        "TestPGChatDirectLifecycleAndCrossPathPrivacy",
        "TestPGChatDirectConcurrentPairAndRollback",
        "TestPGChatRealmModerationAndRevocationAcrossReaders",
        "TestPGChatTimeoutAndCallAdmissionRecovery",
        "TestPGChatRealmTransferAndMissingCallPolicy",
        "TestPGChatRelationsDisableAndProofDuringLockWait",
        "TestPGChatRelationsDeletionAndRestartPersistence",

        "TestPGBackendReaderComposition",
        "TestPGReaderCacheLifecycleAndIsolation",
        "TestPGReaderInvalidationFencesInflightFetch",
        "TestPGReaderCacheCapacityAndAtomicRollback",
        "TestPGReaderInvalidationFreshAuthorityAndPool",
        "TestPGReaderRestartPersistence",

        "TestPGOAuthPreviewRechecksRegistrationAndOwnerVersion",
        "TestPGOAuthConfidentialPurposeScopeWithdrawal",
        "TestPGBackendOAuthComposition",
        "TestPGBackendFortuneComposition",
        "TestPGOAuthAuthorizationCodePKCEConsentAndUserInfo",
        "TestPGOAuthRedirectStateNonceAndParentBindings",
        "TestPGOAuthConcurrentCodeAndRefreshReuse",
        "TestPGOAuthAppOwnerConsentAndSecretRevocation",
        "TestPGOAuthParentRevocationAndTokenExpiry",
        "TestPGOAuthAtomicRollbackAndCanonicalProof",
        "TestPGOAuthOwnerDisableDuringLockWait",
        "TestPGOAuthOpposingOwnersAndRetryableGuardBoundary",
        "TestPGOAuthScopeMinimizationAndBoundedCleanup",
        "TestPGOAuthRestartPersistence",

        "TestPGFortuneConfiguredCatalogueAndDailyIsolation",
        "TestPGFortuneEnqueueAtomicityAndFreshAuthority",
        "TestPGFortuneRetryLeaseRecoveryAndImmutableCompletion",
        "TestPGFortuneWorkerRevocationAndStaleLease",
        "TestPGFortuneConcurrentCheckInAndProfileSnapshot",
        "TestPGFortuneRestartPersistence",

        "TestPGBackendPostRealmComposition",
        "TestPGPostRealmAssociationLifecycleAndFeeds",
        "TestPGPostRealmMediaSurveyInteractionPrivacy",
        "TestPGPostRealmRevokeAndDeleteOrdering",
        "TestPGPostRealmMoveReferenceRace",
        "TestPGPostRealmOppositeMovesAndRollback",
        "TestPGPostRealmFederationWithdrawal",
        "TestPGPostRealmRestartPersistence",

        "TestPGBackendBotMiniappComposition",
        "TestPGBackendCalendarComposition",
        "TestPGBotsCanonicalLifecycleAndCommands",
        "TestPGBotsScopeIsolationHTTPAndFreshAuthority",
        "TestPGBotsQuotaConcurrencyAndAtomicRollback",
        "TestPGBotsOpposingEditorsAndOwnerDisable",
        "TestPGBotsRevocationAndControlledDeactivation",
        "TestPGBotsPolicyTimeBoundariesAndPublisherRevocation",
        "TestPGBotsMiniappCanonicalPackagesAndPrivacy",
        "TestPGBotsRestartPersistence",
        "TestPGNotableDayCatalogLunarAndStableLookup",
        "TestPGCalendarPrivacySubscriptionsAndSearch",
        "TestPGCalendarRecurrenceMutationAndHTTPBounds",
        "TestPGCalendarCanonicalActivityOverlayPrivacy",
        "TestPGCalendarAtomicityAndFreshAuthority",
        "TestPGCalendarRestartPersistence",

        "TestPGRealmPublisherCreationRolesQuotaAndMetadata",
        "TestPGRealmPublisherPostTimelineNestedPrivacy",
        "TestPGRealmPublisherMediaSurveyStickerAndFederationPrivacy",
        "TestPGRealmPublisherLockWaitRechecks",
        "TestPGRealmPublisherOppositeRepliesAndRollback",
        "TestPGRealmPublisherRestartPersistence",
        "TestPGRealmPublisherUnsupportedPublicIntegrations",

        "TestPGTimelineOppositePublisherRepliesAndRollback",
        "TestPGTimelinePrelocksRecheckSourceAndTarget",
        "TestPGTimelinePublisherUUIDSelectorCollision",

        "TestPGBackendTicketsExamProgressionComposition",
        "TestPGTicketsPrivateSupportLifecycle",
        "TestPGExamsSnapshotsClaimsAndPrivateAnswers",
        "TestPGExamsManualReviewTrialsAndLimits",
        "TestPGExamsIndependentPoolIdempotencyAndAtomicClaims",
        "TestPGExamsFreshAuthorityAndDisabledIsolation",
        "TestPGTicketsExamsRestartPersistence",
        "TestPGExamsRewardSinkAtomicAndIdempotent",

        "TestPGBackendMerchantCatalogComposition",
        "TestPGMerchantsBindingOwnershipAndCanonicalCheckout",
        "TestPGMerchantsCatalogRoundTripAndDisable",
        "TestPGMerchantsReportsExactAndIsolated",
        "TestPGMerchantsCatalogConcurrentUpsertAndRollback",
        "TestPGMerchantsScopedCatalogRejectsDeadAncestry",
        "TestPGMerchantsAdminWaitRevocationAndHTTP",
        "TestPGMerchantsAdminScopeRecheckedAfterWait",
        "TestPGMerchantsRestartPersistence",

        "TestPGBackendPresenceProgressionLocationComposition",
        "TestPGPresencePrivacyLeasesAndAuthority",
        "TestPGPresenceFriendsBlocksInvisibleAndExpiry",
        "TestPGProgressionConcurrentCheckInAndIsolation",
        "TestPGProgressionCanonicalProjectorRollbackAndEarnedBadges",
        "TestPGPresenceProgressionRevocationWhileBlocked",
        "TestPGPresenceProgressionRestartPersistence",
        "TestPGProgressionChatSocialProjection",

        "TestPGLocationsMeetLifecycleAndPrivacy",
        "TestPGLocationsMeetDeduplicationAndAuditRollback",
        "TestPGLocationsPinsSessionPrivacyRateAndExpiry",
        "TestPGLocationsMeetPinPrivacyAndBlock",
        "TestPGLocationsNFCOwnershipAndResolution",
        "TestPGLocationsPinMutationRollbackAndRevokedProof",
        "TestPGLocationsHTTPStreamCompletionAndRevocation",
        "TestPGLocationsRestartPersistence",
        "TestPGCallsLifecycleAuthorityAndReplay",
        "TestPGCallsRemovalFailureBanAndRetry",
        "TestPGCallsConcurrentStartJoinAndRestart",
        "TestPGCallsCanonicalRemovalSessionRevocationAndExpiry",
        "TestPGCallsProviderPreparationRecoveryAndHTTPBoundaries",
        "TestPGCallsWaiterObservesCommittedMembershipAndSessionRevocation",
        "TestPGFederationCanonicalPublicHTTP",
        "TestPGFederationInboxReplayAndTargetIsolation",
        "TestPGFederationRemoteObjectsAndFollowControl",
        "TestPGFederationDurableDeliveryAndPrivacy",
        "TestPGFederationFreshAuthorityAndAtomicRollback",
        "TestPGFederationSignedLoopbackDelivery",
        "TestPGFederationRestartPersistence",
        "TestPGBackendCallsFederationComposition",
        "TestPGBackendCallsFederationRestartPersistence",

        "TestPGMediaUsageDirectAtomicMetadata",
        "TestPGMediaUsageResumableAndPostProjection",
        "TestPGMediaUsageRestartPersistence",
        "TestPGMediaUsageAdditiveMigrationDefaults",

        "TestPGBackendContentComposition",
        "TestPGSurveysLifecyclePrivacyAndValidation",
        "TestPGSurveysLinkedPostVisibilityAndDeadline",
        "TestPGSurveysConcurrentReplacementAndRollback",
        "TestPGSurveysVerifiedHTTPAndRevocation",
        "TestPGSurveysArchiveWinsQueuedResponse",
        "TestPGSurveysPublisherRevocationWhileWaiting",
        "TestPGSurveysAuthorityRevocationWhileWaiting",
        "TestPGStickersOwnershipContentAndMediaPrivacy",
        "TestPGStickersCapacityConcurrencyAndAtomicBatch",
        "TestPGStickersVerifiedHTTPAndRevocation",
        "TestPGSurveysStickersRestartPersistence",

        "TestPGTimelineFeedSearchPrivacyAndCursor",
        "TestPGTimelineReplyForwardThreadChainLifecycle",
        "TestPGTimelineFollowsPendingIsolationAndConcurrency",
        "TestPGTimelineDiscoveryFeedbackAndAuditRollback",
        "TestPGTimelineWatchCanonicalPrivacyAndAtomicPatch",
        "TestPGTimelineGuardedHTTPAndFreshAuthority",
        "TestPGTimelineRestartPersistence",
        "TestPGTimelineRelationRollbackDepthAndCancellation",
        "TestPGPostMediaComposeUploadAttachDownload",
        "TestPGPostMediaVisibilityAndRevocation",
        "TestPGPostMediaAtomicOwnershipAndReuse",
        "TestPGPostMediaAuthorityAndFileLockOrdering",
        "TestPGPostMediaIndependentPoolRaceAndMissingBytes",
        "TestPGPostMediaRestartPersistence",

        "TestPGDomainTransactionAuthority",
        "TestPGTaxonomyPublisherMembershipGuard",
        "TestPGBackendSigninHTTPProcessRestart",
        "TestPGVerificationAdaptersHTTPFlow",
        "TestPGVerificationSMTPRequiredTLSAndFailure",
        "TestPGVerificationDeliveryFailureRemainsRecoverable",
        "TestPGSigninLifecycle",
        "TestPGSigninTOTPAndRecovery",
        "TestPGSigninRateConcurrency",
        "TestPGSigninHTTP",
        "TestPGSigninAuthorityBoundary",
        "TestPGSigninRestartPersistence",

        "TestPGCommerceOrderSettlementAndIsolation",
        "TestPGCommerceConcurrentPaymentStockAndRollback",
        "TestPGCommerceSubscriptionRenewCancelAndGiftRedeem",
        "TestPGCommerceRestartPersistence",
        "TestPGCommerceHTTPPaymentAndUnavailableProvider",
        "TestPGCommerceExpiryAndAuthorityOrdering",
        "TestPGMediaResumableLifecycleAndIsolation",
        "TestPGMediaResumableQuotaAndIdempotencyConcurrency",
        "TestPGMediaResumableBoundsHashFailureAndCleanup",
        "TestPGMediaResumablePublicationRecovery",
        "TestPGMediaResumableExpiryCancellationAndFreshAuthority",
        "TestPGMediaResumableHTTPMultipartAndAsyncContract",
        "TestPGMediaResumableRestartPersistence",

        "TestPGNotificationsInboxOutboxIsolation",
        "TestPGNotificationsDeliveryRetryAndLeaseRecovery",
        "TestPGNotificationsConcurrentIdempotency",
        "TestPGNotificationsProjectionRollbackAndDisabledAccount",
        "TestPGNotificationsWalletInboxProjected",
        "TestPGNotificationsWalletAndChatProjection",
        "TestPGNotificationsWebSocketReplayAndRevocation",
        "TestPGNotificationsRestartPersistence",
        "TestPGDeveloperAppsLifecycle",
        "TestPGDeveloperAppsIsolationAndRevocation",
        "TestPGDeveloperAppsRestartPersistence",
        "TestPGDeveloperAppsConcurrentRevocation",

        "TestPGMediaLifecycleAndIsolation",
        "TestPGMediaQuotaAndIdempotencyConcurrency",
        "TestPGMediaRollbackCleanupAndRecovery",
        "TestPGMediaHTTPDirectAndRange",
        "TestPGMediaFreshAuthorityAndCancellation",
        "TestPGMediaAuthoritySerialization",
        "TestPGMediaMetadataFailureAfterObjectWrite",
        "TestPGMediaRestartPersistence",

        "TestPGSocialRelationships",
        "TestPGSocialRealms",
        "TestPGSocialTransactionsAndHTTP",
        "TestPGSocialRestartPersistence",
        "TestPGSocialRealmWriteRevocationOrdering",
        "TestPGPostInteractionsReactionBookmarkPrivacy",
        "TestPGPostInteractionsCollectionOwnershipAndBatch",
        "TestPGPostInteractionsCanonicalTaxonomyAndAtomicLinks",
        "TestPGPostInteractionsIndependentPoolRaces",
        "TestPGPostInteractionsAuditRollback",
        "TestPGPostInteractionsAuthenticatedHTTPAndRevocation",
        "TestPGPostInteractionsRestartPersistence",
        "TestPGPostInteractionsMembershipRevocation",
        "TestPGPostInteractionsDeletedAndDisabledIsolation",
        "TestPGPostInteractionsSocialVisibility",

        "TestPGAuthorityAtomicWrites",
        "TestPGChatRoomTextLifecycle",
        "TestPGChatMembershipPrivacyAndPolicy",
        "TestPGChatConcurrentSequencesAndRollback",
        "TestPGChatConcurrentRemovalAndSend",
        "TestPGChatAuthorityAndUnsupportedBoundaries",
        "TestPGChatRestartPersistence",
        "TestPGAuthorityLifecycle",
        "TestPGAuthorityRestartPersistence",
        "TestPGAccountsPublisherLifecycle",
        "TestPGAccountsRestartPersistence",
        "TestPGAccountsProfileConcurrencyAndCanonicalLookup",
        "TestPGAccountsAtomicRollbackAndQuotaRace",
        "TestPGAccountsProcessPersistence",
        "TestPGWalletHTTPFamily",
        "TestPGWalletAtomicPrecisionRollbackAndIdempotency",
        "TestPGWalletConcurrentDebitAndDuplicate",
        "TestPGWalletHeldLifecycleAndExpiry",
        "TestPGWalletConcurrentQuotaDefaultAndConfirm",
        "TestPGWalletRestartPersistence",
        "TestPGWalletDurablePINPolicyAndLockout",
        "TestPGWalletFreshRealmGrantPolicy",
        "TestPGWalletVerifiedAuthorityComposition",
        "TestPGBackendDomainComposition",
        "TestPGBackendFreshAuthorityAndIndependentPools",
        "TestPGBackendHTTPProcessRestart",
        "TestPGBackendRestartPersistence",
        "TestPGPostsLifecycleAndPrivacy",
        "TestPGPostsVisibilityAndStateGuards",
        "TestPGPostsAtomicRollbackAndCancellation",
        "TestPGPostsIndependentPoolConcurrency",
        "TestPGPostsMembershipRevocationSerializesWithWrites",
        "TestPGPostsAuthenticatedHTTPAndFreshAuthority",
        "TestPGPostsRestartPersistence",
        "TestPGOriginalSchemaAndIsolation",
        "TestPGIndependentServicePatchConcurrency",
        "TestPGSoftDeleteAndDuplicateLiveRows",
        "TestPGTransactionRollbackAndCancellation",
        "TestPGRestartPersistence",
        "TestPGIdentityAndTimestampPreservation",
        "TestPGWatcherRecipientsAndPreferences",
        "TestPGWatcherSoftDeleteAssumptions",
        "TestPGWatcherOneHopChains",
        "TestPGWatcherInputErrors",
        "TestPGBoardRoundTripIsolation",
        "TestPGBoardAtomicRollback",
        "TestPGBoardTransactionConcurrency",
        "TestPGBoardPolicyAndErrors",
        "TestPGBoardRestartPersistence",
        "TestPGDevelopBotCommandsPayloads",
        "TestPGDevelopBotCommandsPolicyAndErrors",
        "TestPGDevelopBotCommandsRestartPersistence",
        "TestPGDevelopPackageDownloadBranches",
        "TestPGDevelopPackagePolicyAndFailures",
        "TestPGDevelopPackageAtomicCounter",
        "TestPGDevelopPackageRestartPersistence",
        "TestPGNodeInfoCountsAndHTTP",
        "TestPGNodeInfoPolicyAndReadOnly",
        "TestPGNodeInfoFailurePropagation",
        "TestPGNodeInfoRestartPersistence",
        "TestPGDevelopDiscoveryPagingAndCounts",
        "TestPGDevelopDiscoverySearch",
        "TestPGDevelopDiscoveryPolicyAndReadOnly",
        "TestPGDevelopDiscoveryRestartPersistence",
        "TestPGBoardHTTPFamily",
        "TestPGBoardHTTPProcessRestart",
        "TestPGBoardHTTPRestartPersistence",
        "TestPGBoardHTTPValidationRollback",
        "TestPGMessagerHTTPProcessRestart",
        "TestPGMessagerHydrationAndMessageReads",
        "TestPGMessagerLimitAndExactInt64",
        "TestPGMessagerMembershipAndVisibility",
        "TestPGMessagerReadOnlyFailuresAndIndependentPool",
        "TestPGMessagerRestartPersistence",
        "TestPGMessagerTimestampSequenceAndRangeRecovery",
        "TestPGMessagerUnreadMatchesRetainedPolicy",
        "TestPGTaxonomyAdminFiltersAndTombstones",
        "TestPGTaxonomyConcurrentClaimAndQuota",
        "TestPGTaxonomyConcurrentCreateAndSubscribe",
        "TestPGTaxonomyCreateUpdateConstraints",
        "TestPGTaxonomyHTTPProcessRestart",
        "TestPGTaxonomyListSearchOrder",
        "TestPGTaxonomyOwnershipQuotaEvent",
        "TestPGTaxonomyPolicyErrorsAndReadOnly",
        "TestPGTaxonomyRestartPersistence",
        "TestPGTaxonomySubscriptionsAndDeleteRollback",
        "TestPGTaxonomyTransactionalAudit",
    },
    "after": {
        "TestPGLiveStreamChatOutboxRestartPersistence",

        "TestPGFederatedRenameRestartPersistence",
        "TestPGAdminExtensionsRestartPersistence",

        "TestPGLiveStreamCleanupRestartPersistence",

        "TestPGContentModerationRestartPersistence",

        "TestPGNameCardRestartPersistence",
        "TestPGRewindRestartPersistence",

        "TestPGAccountSettingsRestartPersistence",

        "TestPGTranslationRestartPersistence",
        "TestPGAutocompleteRestartPersistence",

        "TestPGChatRelationsDeletionAndRestartPersistence",

        "TestPGReaderRestartPersistence",

        "TestPGOAuthRestartPersistence",

        "TestPGFortuneRestartPersistence",

        "TestPGPostRealmRestartPersistence",

        "TestPGBotsRestartPersistence",
        "TestPGCalendarRestartPersistence",

        "TestPGRealmPublisherRestartPersistence",

        "TestPGTicketsExamsRestartPersistence",

        "TestPGMerchantsRestartPersistence",

        "TestPGPresenceProgressionRestartPersistence",

        "TestPGLocationsRestartPersistence",
        "TestPGFederationRestartPersistence",
        "TestPGBackendCallsFederationRestartPersistence",

        "TestPGMediaUsageRestartPersistence",

        "TestPGSurveysStickersRestartPersistence",

        "TestPGTimelineRestartPersistence",
        "TestPGPostMediaRestartPersistence",

        "TestPGSigninRestartPersistence",
        "TestPGCommerceRestartPersistence",
        "TestPGMediaResumableRestartPersistence",

        "TestPGNotificationsRestartPersistence",
        "TestPGDeveloperAppsRestartPersistence",

        "TestPGMediaRestartPersistence",
        "TestPGSocialRestartPersistence",
        "TestPGPostInteractionsRestartPersistence",

        "TestPGChatRestartPersistence",
        "TestPGAccountsRestartPersistence",
        "TestPGAuthorityRestartPersistence",
        "TestPGBackendRestartPersistence",
        "TestPGBoardHTTPRestartPersistence",
        "TestPGBoardRestartPersistence",
        "TestPGDevelopBotCommandsRestartPersistence",
        "TestPGDevelopDiscoveryRestartPersistence",
        "TestPGDevelopPackageRestartPersistence",
        "TestPGMessagerRestartPersistence",
        "TestPGNodeInfoRestartPersistence",
        "TestPGPostsRestartPersistence",
        "TestPGRestartPersistence",
        "TestPGTaxonomyRestartPersistence",
        "TestPGWalletRestartPersistence",
    },
}


EXPECTED_PACKAGE = "solsynth.local/go-migration/tests/integration"


def check(phase, events):
    expected = EXPECTED[phase]
    passed = set()
    started = set()
    package_passed = False
    for event in events:
        if not isinstance(event, dict):
            raise ValueError("invalid go test event")
        name, action = event.get("Test", ""), event.get("Action")
        if not isinstance(name, str):
            raise ValueError("invalid test name")
        if action == "fail" or (name.startswith("TestPG") and action == "skip"):
            raise ValueError(f"integration failed or skipped: {name or event.get('Package')}")
        if name.startswith("TestPG"):
            top = name.split("/", 1)[0]
            if top not in expected:
                raise ValueError(f"unexpected PostgreSQL case in {phase} phase: {top}")
            if event.get("Package") != EXPECTED_PACKAGE:
                raise ValueError("PostgreSQL result came from the wrong package")
            if name == top and action == "run":
                if name in started:
                    raise ValueError("duplicate PostgreSQL case: " + name)
                started.add(name)
            if name == top and action == "pass":
                if name not in started or name in passed:
                    raise ValueError("PostgreSQL pass without one matching run: " + name)
                passed.add(name)
        if not name and action == "pass" and event.get("Package") == EXPECTED_PACKAGE:
            package_passed = True
    missing = expected - passed
    if missing:
        raise ValueError("missing required passes: " + ", ".join(sorted(missing)))
    if not package_passed:
        raise ValueError("missing successful integration package completion")
    return len(expected)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--pattern":
            print("^(" + "|".join(sorted(EXPECTED[sys.argv[2]])) + ")$")
            sys.exit(0)
        phase, path = sys.argv[1:]
        with open(path, encoding="utf-8") as handle:
            count = check(phase, [json.loads(line) for line in handle if line.strip()])
        print(f"Verified {count} required PostgreSQL case(s) passed in {phase} phase; none skipped")
    except (ValueError, KeyError, OSError) as error:
        sys.exit(str(error))
