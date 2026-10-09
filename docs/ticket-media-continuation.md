# Ticket attachment lifecycle

This continuation starts at `525ef9e2c80565d323108e41e0cc3e53793413f7` and composes
ticket messages with canonical media. It does not introduce file storage, quota,
identity, notification delivery, or an account-activation authority.

## Authored evidence and boundaries

The retained licensed `TicketController.cs` declares `file_ids` on
`POST /api/tickets` and `POST /api/tickets/{id}/messages`. Its authored service
resolves those IDs and returns file references in each message's `files` field,
including the initial ticket message. Existing `/passport` and `/stargate`
aliases remain provided by the host. The retained Solian ticket-detail screen
uploads with the descriptive `ticket` usage label and renders `message.files`.
Exact source paths, hashes, and limitations are recorded in
`ticket-media-source-evidence.json`; the original complete snapshots and their
existing manifests remain unchanged.

The controller permits creator, current assignee, or ticket operator reads.
Replies require the creator or a current `tickets.admin` operator plus the
existing `tickets.messages.create` permission. Assignment alone does not grant
reply authority. Metadata changes, assignment, status changes, and ticket
deletion keep their existing route permissions and operator checks. No newer
numeric status alias is inferred from an excluded model.

There is an explicit caller/controller disagreement: the Solian composer permits
an attachment-only reply, while the retained controller marks reply content
required. This continuation keeps nonempty content, documenting that remaining
compatibility gap rather than silently weakening the established text contract.
The retained source has no message edit/delete or persisted per-file detach
route. No such endpoint is invented: deleting the ticket withdraws its attachment
grants, and canonical file deletion withdraws that source from every projection.
Removing a locally selected attachment before sending remains a client action.

## Canonical ownership and current audience

`owned_ticket_media` stores ordered message/file links only, with at most 32
distinct canonical UUID file IDs per message. The uploader must be the message
author, even when an operator or participant can read somebody else's file.
A public file owned by another account is also not accepted for redistribution
through a ticket. File usage is descriptive and grants no audience by itself.
Selecting a file never changes its canonical `is_public` flag, owner, quota, or
byte location. Arbitrary canonical file MIME types remain governed by the
existing media serving contract; this delivery does not claim content scanning.

Attachment selection locks ready canonical files in sorted UUID order within
the existing account/ticket transaction, then checks ownership and active owner
state. A file-row waiter observes current source state. Text, attachment links,
ticket timestamps, and audit commit or roll back together. Idempotency includes
the attachment list; an unchanged retry returns the original message with its
current visible files, and a changed list conflicts. Existing text-only message
idempotency hashes remain compatible.

Every file projection uses current canonical readiness and active owner state.
Private byte access follows a current live ticket and its creator/assignee/operator
policy, checked in the canonical media transaction with original-proof
preflight and no additional locks or pool connections. Anonymous callers gain
no ticket audience. Lost assignment, lost operator permission, case deletion,
creator disablement/punishment, or source deletion withdraws the corresponding
grant. Uploader ownership and unrelated independently valid media grants retain
their own rules. A previously delivered byte stream cannot be recalled.

Ticket operations recheck original authority before commit; staff replies and
administrative writes also recheck `tickets.admin`, so a file wait cannot extend
a token or operator grant. Assignment takes its actor/assignee account locks in
UUID order before the case row, preventing an account foreign-key inversion.
Writes retain PostgreSQL microsecond timestamp precision. No role, account ID,
or file ownership asserted in request metadata becomes proof.

## Installation and composition

Apply `internal/ticketmediapg/schema.sql` after the existing ticket and media
schemas. The guarded installer and dependency-order check include it explicitly.
The existing atomic media-usage upgrade expands its constraint with `ticket`.
Constructors never install schemas.

When media storage is configured, the host creates one ticket-media adapter,
installs it on the canonical ticket repository, and composes it alongside post
and chat audiences under the existing profile-media wrapper. Readiness probes
the reference relation. A standalone text-only ticket composition without a
media adapter returns unsupported for supplied valid file IDs. Existing text
support and examination/activation behavior remains unchanged.

## Verification

Five new required PostgreSQL scenarios in `ticket_media_test.go` cover composed
multipart upload/HTTP/bytes, private audiences and assignment, operator revocation,
disabled creators, ownership and foreign-file redistribution denial, strict IDs,
idempotency, audit rollback, source/ticket deletion, file-row waits, proof expiry,
assignment withdrawal during a blocked download, and before/after restart
persistence through actual canonical storage. Tests use explicit JSON or multipart
content types and synthetic actors/files only. File assertions use message IDs
where fixture clocks can produce equal timestamps.

Local build, vet, tests/race tests, source integrity, schema/wiring guards and the
pinned PostgreSQL-16 parser are run offline. Guarded database cases compile and
skip without `PG_TEST_DSN`; their success is not claimed until the parent's private
PostgreSQL CI runs them. No database/socket retry, live user/provider call,
external transmission, or publication occurs in this work.

Activation remains an unresolved authority integration: meeting exam requirements
can record a pending request but never enables an account. This attachment flow
does not alter that boundary. Original SDK DTO/nanoid parity, attachment-only
reply disagreement, unmapped status values, per-file detach, and external delivery
remain explicit gaps.
