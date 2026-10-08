# Publisher handles and durable federation identity

This independently authored continuation removes the registered-publisher name-card restriction after the publisher has an explicit stable identity anchor. It builds on continuation `3c537d4`, the frozen account-extras patch, and the frozen content-moderation patch. Existing name-card payment, settlement, consumption, role checks and idempotency remain the authority for ordinary paid rename. The separate moderation route retains its current Superuser plus `publishers.update` policy. Display-name (`nick`) edits need no handle reservation.

## Identity and lookup behavior

`owned_publisher_name_reservations` permanently binds each folded handle to one canonical publisher UUID. An insert/name-update trigger covers all publisher creation and rename paths. Reservations survive soft deletion, and a foreign UUID cannot reclaim an old name. One publisher may reuse its own reserved names. The cap is 256 distinct folded names, including the current name. Alias history is not a public API; prefix autocomplete still searches current names only.

`owned_federation_publishers.identity_handle`, `actor_uri` and `key_id` anchor the original actor URL and key identifier. Existing key providers continue to select the signer by publisher UUID. Rename generates no key material and changes neither identity nor queued activity payloads. Immutable triggers prohibit changing or deleting anchors. New registrations anchor to their configured origin inside the normal settings transaction. Old registrations require the explicit initializer below before rename or enabled public federation access.

Both current and retired handles resolve through current account, publisher, realm, post and moderation gates. This applies to canonical publisher reads, post slug reads/listing, timeline/follows/feed selectors, collections, surveys, stickers, developer apps, bots and moderation selectors. Ordinary metadata updates cannot perform a paid handle change. Name-card consumption, alias reservation, publisher update and audit commit together; rollback restores all of them. Current account/realm/publisher locks precede name reservation uniqueness waits, with authority rechecked before commit.

Actor responses at cached URLs retain their original `id`, inbox and key `owner`/`id`, while `preferredUsername` reflects the current handle. WebFinger returns the current account subject, stable actor link and the queried retired alias only. Reads use `Cache-Control: no-store`. Inbox proofs must match the exact requested alias URL; a proof for another URL is not accepted. Inbox deduplication remains publisher UUID plus activity ID. Disabled, deleted and shadowbanned identities are hidden through both old and current handles. Public-to-private posts disappear from old slug and federation object reads.

## Explicit migration and operator invocation

Apply the canonical authority/account schemas, then `internal/accountpg/name_schema.sql`. Apply canonical posts and the base federation schema before `internal/federationpg/identity_schema.sql`. Name-card and moderation schemas remain prerequisites for their respective callers. Exported `accountpg.Schema` and `federationpg.Schema` include their additions; constructors never install them. The disposable PostgreSQL runner and its offline ordering checker include both SQL files.

The callable operator APIs are `Repository.InitializeIdentity(ctx, publisherUUID)` and `Repository.InitializeIdentities(ctx, limit)` with limit 1–100. The caller explicitly constructs `federationpg.New(db, config, nil, nil)` using the exact existing canonical origin, checks `Ready`, then invokes the method. The initializer loads no key provider, enables nothing and performs no remote communication. It locks owner account, publisher and registration in order. Each identity commits independently; a batch error returns the number committed so far. Repeating the same origin is idempotent. A different origin rejects existing anchors, including the batch-wide preflight. An unanchored registration retains its migration-pinned original handle and must own that reservation.

The backend contains a separate operator subcommand. Validation alone does not read a DSN or open a database:

```sh
backend initialize-federation-identities --origin=https://your-existing-origin.example
```

After separately applying the prerequisites and explicitly injecting the existing operator DSN into the named environment variable, one bounded mutation batch is:

```sh
backend initialize-federation-identities --origin=https://your-existing-origin.example --dsn-env=SOLSYNTH_BACKEND_DSN --limit=100 --timeout=30s --apply
```

Repeat explicit batches until the command reports zero. For a single identity, add `--publisher=<canonical-uuid>`. The existing backend DSN policy rejects ambient PostgreSQL settings, credentials discovered from files and insecure remote TLS settings. Errors do not print the DSN. The command starts no HTTP server or worker and creates no account, permission, key or schema. Normal backend startup never invokes it. A domain move requires a separate migration design; it cannot be accomplished by reinitializing these anchors.

## Evidence and verification

The retained full licensed name-card service and publisher/ActivityPub/WebFinger controllers establish the existing paid rename and publisher federation flows. Exact source hashes remain in their existing provenance manifests and are referenced by `federation-rename-source-evidence.json`; no source files are copied by this delivery. The bounded alias policy and explicit migration command are owned compatibility behavior, not a claim that the original service implemented these protections.

The [W3C ActivityPub actor model](https://www.w3.org/TR/activitypub/#actor-objects) distinguishes actor identity from presentation properties. [RFC 7033 section 4.4.1](https://datatracker.ietf.org/doc/html/rfc7033#section-4.4.1) allows a subject different from the queried resource, and [section 4.4.2](https://datatracker.ietf.org/doc/html/rfc7033#section-4.4.2) defines aliases for the same entity. These support keeping the actor URL stable while returning a changed handle; they do not prescribe this application's cap or reservation schema.

Six new guarded PostgreSQL cases cover paid replay and cached actor/key/post URLs, explicit initialization and changed origin, concurrent claims and cap/reuse, queued deliveries and current privacy, rollback and privileged rename, and process restart. Existing signed-inbox and moderation-withdrawal tests now exercise anchored rename. The new delivery fixture has a public-only synthetic signer and in-memory transport; it cannot make a valid RSA signature. The inbox regression reuses its existing synthetic fixture signer. Local checks execute no PostgreSQL/socket/provider operation. Actual PostgreSQL and restart results are pending the parent's guarded CI run.

This slice adds no remote Move activity, remote alias discovery, alias enumeration, origin migration or organization federation support. Existing content-free moderation withdrawals remain intact.

## Integrated host candidate

The current combined host preserves admin verification fields while mounting the initializer subcommand and both explicit schema additions. A composed PG case crosses existing paid card fulfillment/use, old and current actor URLs, canonical publisher aliases and independent display-name edits. It asserts unchanged actor/key IDs; current runtime privacy remains enforced. Seven added guarded cases including that host case await exact-commit CI; prior348/48 evidence does not validate this migration.
