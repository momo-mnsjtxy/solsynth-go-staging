# Surveys and sticker continuation

This is a working owned PostgreSQL implementation derived from pinned, licensed authored behavior. It is not a claim of complete inherited EF model, production-provider or excluded binding parity. No third-party media assets, generated SDK files, SoSYS, Spec or NeTo sources were used. All test accounts, responses, media bytes and keys are synthetic.

## Provenance and recovered contracts

`ports/sphere/{surveys,stickers}/source/manifest.json` pins every retained file and SHA-256. DysonNetwork sources are at `4ee2d0cebb9da8284a92483fee6dce795bf6947c`; authored Solian callers are at `ffd0838e5f04cb40bea311a39118ebeedad210c1`. Each source directory contains the applicable AGPL license copies.

- `SurveyController.cs`, `SurveyService.cs` and `docs/SURVEY_API.md` establish publisher editor management, draft/published/archived lifecycle, immutable published questions, clone semantics, account-specific response replacement, required/optional question validation, deadline behavior, feedback permissions, anonymous hydration and hidden results.
- The authored Survey API document explicitly maps question types: single choice 0, multiple choice 1, yes/no 2, rating 3, free text 4; status: draft 0, published 1, archived 2. These values were recovered without inspecting excluded shared models or generated bindings. Solian editors use the corresponding enum index and establish snake-case request fields and option-ID answer values.
- `StickerController.cs`, `StickerService.cs`, Solian's authored sticker model and creator pack editor establish ASCII-alphanumeric prefixes/slugs, publisher owner creation, editor management, 24 items per pack, identifier lookup, pack acquisition, ordering and batch rendering. Solian explicitly maps sizes auto/small/medium/large to 0/1/2/3 and sticker/emote modes to 0/1.

## Runtime and schema

Install `internal/surveypg/schema.sql` and `internal/stickerpg/schema.sql` explicitly after canonical authority, accounts/publishers, posts and media schemas. Runtime constructors never migrate or seed:

- Surveys: `owned_surveys`, `owned_survey_answers`, `owned_survey_audit`.
- Stickers: `owned_sticker_packs`, `owned_stickers`, `owned_sticker_ownerships`, `owned_sticker_audit`.
- Survey questions/options are validated ordered JSONB, with fresh UUIDs when omitted or all-zero. Response uniqueness is a database `(survey_id, account_id)` constraint. Replacing a response preserves its ID and creation timestamp.
- Sticker prefixes are unique case-insensitively; slugs are unique case-insensitively within a pack. Soft-deleted identifiers remain reserved. Pack acquisition is idempotent and database unique per account. A locked pack serializes capacity, rendering, ordering and content changes across independent pools.

Use `surveypg.NewWithAuthority(db, guard).WithVisibility(socialRepo).WithMedia(mediaRepo)` and the equivalent `stickerpg` constructor in the unified host. Mount `surveys.NewHandler` and `stickers.NewHandler` with its existing verified Auth adapter. Both repositories implement `BindAuthority`; standalone recorder tests use each port's `NewVerifiedHandler`.

The unguarded `New(db)` constructor follows existing repository test patterns and is for trusted in-process fixtures. HTTP composition must use `NewWithAuthority`; passing a nil guard fails closed. Actor UUIDs never establish HTTP identity.

Every authenticated operation uses a read-committed transaction and canonical authority-account lock. Mutations revalidate the bound signed proof and operation permission in that transaction. Publisher editor checks reuse `accountpg.RequirePostEditor`/`ResolveForPost` and retain the publisher lock through commit. Survey responses lock the survey exclusively; results/feedback hold a shared survey lock, so multi-query totals and answers cannot see partial replacements. Audit insertion failure rolls back all associated writes.

## Survey HTTP surface

The port owns `/api/surveys`; unified-host aliases can retain `/sphere/surveys`.

| Method and route | Behavior |
|---|---|
| POST `/api/surveys?pub=…` | Create validated draft |
| PATCH, DELETE `/api/surveys/{id}` | Draft edit or soft-delete with publisher editor access |
| GET `/api/surveys/{id}` | Visible questions, caller's own answer, permitted stats |
| GET `/api/surveys/me` | Member publishers, optional `pub`/`active`, paged `X-Total` |
| POST `/api/surveys/{id}/publish` | Freeze valid draft and set published time |
| POST `/api/surveys/{id}/archive` | Stop accepting published-survey answers |
| POST `/api/surveys/{id}/clone` | Fresh draft with fresh question and option UUIDs |
| POST, DELETE `/api/surveys/{id}/answer` | Replace caller's unique response or withdraw it |
| GET `/api/surveys/{id}/feedback` | Publisher Viewer+ or canonical superuser only |
| GET `/api/surveys/{id}/results` | Additive precise aggregate totals, subject to results privacy |
| POST `/api/surveys/{id}/subscribe`, `/unsubscribe`; GET `/subscription` | Caller-only explicit subscriptions and local answer notification opt-in |

Single choice accepts one valid option UUID string; multiple choice accepts distinct option UUID strings; yes/no accepts a boolean; rating accepts a finite bounded number; text accepts bounded valid UTF-8, with UTF-16 length matching .NET string limits. Optional answers can be missing or null. Unknown question IDs, duplicate options/selections and malformed values are rejected before mutation. Limits are 100 questions, 100 options per choice question, 16 intro attachments, 16,384 UTF-16 text units and a 256 KiB HTTP/response payload bound. Positive `max_selections` cannot exceed available options.

Draft details are editor/admin-only. Linked `post_id` is an additive owned feature: it must reference the same canonical publisher, and reads/responses reuse `postpg.ReadWithVisibility` with the social provider. Private, deleted, blocked and friendship-restricted posts therefore constrain the survey. Management can detach a deleted post without making the survey public accidentally. Standalone published surveys are readable; direct reads still respect publisher-owner disablement, publisher deletion and bilateral social blocking when configured.

Hidden results are visible only to publisher Viewer+ members or canonical superusers. Authenticated participants may retrieve their own answer but never another participant's response or feedback list. Anonymous surveys omit account identifiers from response/feedback transport, including privileged feedback. The database necessarily retains the account key for duplicate prevention; this is transport anonymity, not database anonymity. Public statistics never include free-text values. The documented `stats` map contains option counts and round-to-even integer rating averages; `/results` provides precise averages/counts as an explicitly additive shape.

## Sticker HTTP surface

The port owns `/api/stickers`; unified-host aliases can retain `/sphere/stickers`.

| Method and route | Behavior |
|---|---|
| GET, POST `/api/stickers` | Public pack discovery or owner-created pack |
| GET, PATCH, DELETE `/api/stickers/{pack}` | Pack detail, editor update/delete |
| GET `/api/stickers/by-prefix/{prefix}` | Case-insensitive prefix lookup |
| GET, POST `/api/stickers/{pack}/content` | Ordered content or editor insertion |
| GET, PATCH, DELETE `/api/stickers/{pack}/content/{id}` | Detail or editor update/delete |
| PATCH `/api/stickers/{pack}/content/order` | Atomic reorder of specified pack items |
| PATCH `/api/stickers/{pack}/content/batch/rendering-settings` | All-or-nothing size/mode update |
| GET `/api/stickers/lookup/{identifier}` | Case-insensitive `prefix+slug`, UUID or unambiguous legacy concatenation |
| GET `/api/stickers/lookup/{identifier}/open` | Redirect to existing public Drive byte route |
| POST `/api/stickers/lookup/batch` | At most 100 placeholders, preserving order/misses |
| GET `/api/stickers/search?query=…` | Literal identifier-prefix search and visible total |
| GET, POST, DELETE `/api/stickers/{pack}/own` | Caller-only ownership status/acquire/release |
| GET `/api/stickers/me` | Caller-only acquired packs with current content |
| PATCH `/api/stickers/me/order` | Atomic caller-only acquisition ordering |

Pack acquisition is a personal saved-pack relation, not a grant to private media. The licensed controller has no private-pack property, so this implementation does not invent one. Pack/content reads filter deleted publishers/packs/items, disabled publisher owners and bilateral blocks. Prefix/slug rename has immediate lookup effects because no stale snapshot cache is introduced.

`internal/mediapg/public_transaction.go` adds the approved metadata adapter. It reads only ready, public files with enabled canonical owners and retains a shared file lock through the caller transaction. Stickers accept PNG/JPEG/GIF/WebP/AVIF metadata. Every lookup rechecks current metadata; privatizing/deleting media or disabling its owner removes the image from public reads. No physical bytes are copied into sticker/survey storage. The existing media adapter serves bytes and independently rechecks authorization. SVG/active content is not accepted as sticker imagery.

## Deliberate differences and remaining gaps

- These are bounded owned projections, not complete excluded shared entities. Publisher/media hydration uses existing canonical Go projections. Non-anonymous feedback supplies account IDs without remote account-profile hydration.
- Create/edit/delete require explicit operation permissions in addition to publisher membership. Draft visibility, fully omitted anonymous account IDs, strict unknown/duplicate JSON rejection, duplicate-option rejection, finite caps and exact-deadline closure deliberately tighten the source behavior.
- Survey deletion and sticker deletion are soft, reserve identifiers and retain audit history; response withdrawal removes the current owned answer row. Source uses different soft/hard deletion paths. Cloning resets expired deadline and additive post association, preserving content/settings but avoiding a permanently stale draft link.
- Intro survey attachments resolve only public canonical files. Per-question attachments, popularity buffering and external notification-provider fanout are not implemented. Survey subscriptions and durable local answer notifications are documented in `docs/survey-subscriptions-continuation.md`; `notify_subscribers` is now persisted and supported. Unsupported fields fail before mutation. No failure is presented as successful provider delivery.
- No legacy `poll` route alias, post/chat embed transport, gRPC, object-storage provider, production migration or frontend enum-binding package is claimed. Survey question/status and sticker size/mode numbers are recovered; broader inherited model parity is not.
- Discovery pages are bounded, but the current owned repositories enumerate candidate IDs to apply current cross-domain visibility before totals. Large datasets should replace this with a shared visibility-aware SQL projection before production exposure.

## Verification

Focused validator, strict nested JSON and rendering tests pass under `go test -race`. All integration tests compile. Actual PostgreSQL/restart execution is owned by the parent integration runner; compilation/skips are not runtime evidence.

The integration file `tests/integration/surveys_test.go` covers lifecycle, duplicate response replacement, hidden/anonymous results, private-post/social visibility, deadlines, independent-pool replacement/capacity races, failed audit/batch rollback, actual signed HTTP proof, session revocation, revocation while queued on canonical locks, archive while a response waits, and restart persistence of surveys, acquired stickers and original media bytes.

Run the before phase with `-run 'TestPGSurveys|TestPGStickers'`. After a real PostgreSQL restart, run only `TestPGSurveysStickersRestartPersistence` with `PG_TEST_PHASE=after`; that branch performs no schema installs, data seeds, tokens or writes. Both phases use the existing strict disposable-cluster marker checks.
