# Wallet accounts and transactions

Source-informed, independently redesigned Go implementation under AGPL-3.0-only.
This is not a clean-room claim and not a complete legacy backend replacement.

## Lawful evidence

The immutable source set and exact Git blob/SHA-256 hashes are in
`source/manifest.json`, pinned independently by `TestRetainedOwnedWalletSourceHashes`.
Original source and license bytes are preserved, including final newlines.

- `Solsynth/DysonNetwork` revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`:
  owned WalletController, WalletService, PaymentService; InitialMigration,
  AddMultiWalletSupport, EnrichWalletTranasction, current owned EF snapshot;
  MULTI_WALLET, WALLET_TRANSACTION_LIFECYCLE and WALLET_PIN_VALIDATION docs.
- `Solsynth/Solian` revision `ffd0838e5f04cb40bea311a39118ebeedad210c1`:
  authored wallet UI, transaction detail, realtime invalidation and PIN parser.
  These call an external SDK. No SDK schema, generated code or unlicensed source
  was acquired or copied. The Go recorder parser is newly authored for the
  evidenced JSON projection; it is not an SDK or client E2E oracle.
- Source expansion retrieved only these root-repository files at the pinned
  revisions through the connected GitHub reader. A speculative `master` tree
  read returned an error; subsequent reads used the known inventory revision.
  No canceled repository access, protocol schemas, descriptors, mirrors,
  unlicensed Suki source, Shared submodule source or provider secrets were used.

## Evidenced contracts

- WalletController: `/api/wallets`, authenticated creation/default/list/specific
  wallet, personal-only default changes, public-ID permission gate, viewer realm
  reads and editor realm changes/spending, target precedence and same-wallet ban.
- WalletService: three personal wallets, automatic first primary, primary-first
  then created ordering, nullable public IDs and `DNW-XXXX-XXXX-XXXX` alphabet.
- PaymentService: positive amounts truncated toward zero to three decimals,
  currency pockets, instant/held transfer states, 24-hour deadlines, confirm,
  reject, expiration and notifications. The current transfer implementation
  creates no fee transaction; historical prose mentioning a fee is not followed.
- Solian's authored status switch establishes numeric Pending=0, Frozen=1,
  Confirmed=2, Refunded=3, Cancelled=4 and transfer type=0. The authored direct
  transfer URL is `/wallet/wallets/transfer`. Its PIN status parser reads
  `has_pin` and `validation_required` at `/stargate/accounts/me/pin-status`.
- Owned migrations establish UUID ownership, nullable account/realm/public ID,
  numeric amounts, timestamps and field lengths. They are evidence only: the
  independent schema is not applied over an existing legacy schema.

## Deliberate independent policy

See `docs/wallet-transactions-continuation.md` for atomicity, outbox, idempotency,
held-total arithmetic, strict frozen settlement, soft-delete, PIN and realm
policy differences. None of these changes are represented as observed legacy
semantics. No successful external payment, notification or delivery is inferred
from an internal ledger commit.
