# Realm publishing CI fixture corrections

The 251-case checkpoint (CI run 37759012544, base 8e223ab2b1e88816d910c01fff9feff5900a20e1) exposed three fixture errors. Runtime permissions, visibility and lock ordering remain unchanged.

- Survey Results always requires authentication. The private-realm test now requires `surveys.ErrUnauthenticated` for an anonymous caller and `surveys.ErrNotFound` for the authenticated outsider, and requires zero result data in both cases. Anonymous survey introductions still require not-found.
- The realm concurrency helper previously combined a new SQL pool with the original fixture's TransactionGuard and signed context. The guard correctly rejected this before the association hook because pools must match exactly. The helper now accepts only the fixture and uses its guarded pool. Each concurrent BeginTx still reserves its own connection, and every existing database lock-wait, NOWAIT, ancestry, visibility, and rollback assertion remains intact.
- A compile-time interface assertion ensures the observing wrapper implements the active AssociationPrelocker signature. Current-tree call sites include post-realm races; the separate 251-compatible patch retains the older hook signature.

Validation: current-tree integration compilation, focused authority/post/timeline package tests and race checks passed; integration vet passed. The current offline schema harness passed 40 schemas, 181 tables and 18 rejection checks. The 251-compatible integration package also compiled. Actual PostgreSQL cases explicitly skipped without the disposable runner; the corrected three cases and post-realm race cases require CI execution. No database, socket, live server or external provider was started locally.

The corrected 251-case run passed 250 cases and reached a later synthetic sticker insertion whose prefix contained hyphens. The fixture now uses an alphanumeric prefix satisfying the existing schema constraint; runtime validation remains unchanged.

The first 279-case composed run passed build/vet/race but stopped during disposable schema installation because OAuth preceded canonical apps. The harness now installs OAuth after apps and automatically checks REFERENCES/ALTER dependencies across all explicit SQL files, with a regression rejecting the previous order. Ordinary checks and PG binary builds share Go's build cache; the pinned setup-go action caches only modules/build outputs, excluding database/runtime fixture directories.
