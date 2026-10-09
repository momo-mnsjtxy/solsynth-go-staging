# Bounded account history continuation

Base: `53c097b8c0ef1c0aba73f8c3b93af1533782862f`.

Six original read routes now compose canonical timeline, actual domain audit,
immutable provenance-required social credit, and established provider-link metadata
projections. See [source contracts and boundaries](../ports/passport/history/PROVENANCE.md).
No frontend work, external provider execution, credential disclosure, reward
writer, provider-link writer, legacy data migration or production deployment is
included. Empty owned credit ledgers return the exact authored base of 100, while
missing storage fails closed.

New maintained SQL is an explicit runner/install input. Eleven guarded cases and
one paired restart case are added to the required registry. Composed tests issue
synthetic original credentials before creating target state. The restart fixture
saves `account-history-state.json` with original credentials, a public verifier/time
snapshot and exact route responses; after restart it restores those original
proofs, compares unchanged data and verifies other-account isolation without
issuance, reseeding, refresh or private keys.

Local compilation and offline verification do not exercise PostgreSQL. Run the
full private disposable CI job set on the eventual immutable composed commit,
including actual database/process restart and all required non-skipped cases.
