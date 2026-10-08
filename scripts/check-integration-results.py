#!/usr/bin/env python3
"""Fail closed unless each required real PostgreSQL test passed, never skipped."""
import json
import sys

EXPECTED = {
    "before": {
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
