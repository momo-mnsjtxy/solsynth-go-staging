# Public bot command read candidate

This source-derived AGPL-3.0-only operation implements
`GET /api/bots/{botId}/commands` under explicit synthetic policy. It reads real
configured command data from PostgreSQL and does not create a chat configuration
when none exists. It is not a production identity or shared-model replacement.

## Authoritative eligible sources

Baseline: `Solsynth/DysonNetwork@4ee2d0cebb9da8284a92483fee6dce795bf6947c`.

- `source/DysonNetwork.Develop__Identity__BotAccountPublicController.cs` establishes
  the current route, UUID constraint, missing-bot 404, nullable-config lookup and
  `config?.Commands ?? []` fallback.
- `product-source/DysonNetwork.Develop__Identity__BotAccountService.cs` establishes
  lookup by bot ID including its project, and a separate non-mutating config
  lookup by that same ID. The public operation does not call account hydration,
  developer publisher lookup, or the mutating `GetChatConfigAsync` method.
- `source/...AppDatabaseModelSnapshot.cs` establishes bot/project IDs, active and
  deleted columns, config ID and required JSONB command storage. There is no
  explicit `is_active` filter in the source read operation, so this candidate
  also returns commands for an inactive, otherwise visible bot.
- `inventory/product-client-evidence/DysonNetwork__docs__BOT_CHAT_API.md`, blob
  `4f490fc6a86d8f8992f51ea2eb5d3bca016b3354`, explicitly documents the complete
  four-field command and four-field parameter DTOs used for this candidate.
  Its SHA-256 is `e86ee2ac0d277ba3ce5d496c6efabcb38805b3c71893dfe7eea8f92ef68c28de`.
- Owned startup sets HTTP JSON property names to SnakeCaseLower. Owned database
  setup enables Npgsql DynamicJson but does not set its JSON property naming.

The API document's older `/api/bots/public/...` routes and webhook examples do
not match the current controller/snapshot. They are not copied as runtime
behavior. This is why the current controller governs route and control flow,
while the document provides command DTO field types. No shared source,
NeTo/Spec schema, descriptor, or generated protocol binding was consulted.

## Documented response fields

Commands contain `name` and `description` strings, nullable `usage` string, and
nullable `parameters` list. Parameters contain a `name` string, nullable
`description` string, `required` boolean, and nullable `type` string. Lists retain
their stored order. An empty parameter array stays `[]`; a null array stays null.
No app, account, project, developer, webhook or configuration metadata is exposed.

The missing-bot error preserves the controller's assigned `code`, `message` and
`status` values: `DEV_BOT_ACCOUNT_NOT_FOUND`, `Bot not found`, 404. Additional
shared ApiError members/converters are not established. Candidate-only failures
use an `error` field and sanitized 503 responses.

## Explicit storage and policy boundary

All ordinary handler/service/repository constructors deny by default. The host
must opt into this family independently. No request header or decoded token
creates an identity. Authorization, Cookie and X-Test-Account header presence is
rejected, even if empty. Global auth/permission and soft-delete implementations
remain unresolved; that does not prevent a documented isolated fixture policy.

Selected SQL policy: bot/project rows must have `deleted_at IS NULL`; an absent
or deleted configuration is treated as missing. The repository joins the required
project but does not hydrate its developer, matching the used source read's
dependency boundary. A single SELECT with a left join gives a coherent snapshot;
source uses two separate awaited queries, so concurrent visibility differs.

Selected JSON storage representation: **documented lowercase wire-shaped JSON**.
It is not claimed to be the production Npgsql representation. JSONB and MVC use
separate serializers, and shared property attributes/default initializers have
not been established. The adapter deliberately does not infer PascalCase keys,
case-insensitive aliases, or a raw passthrough mapping.

- Root JSON null uses the source controller's empty-list fallback.
- Required `name`/`description` strings and parameter `name`/`required` values
  must be explicitly present and non-null. Empty strings remain valid.
- Omitted optional members become null under the selected fixture policy.
- Unknown/mis-cased members, wrong types, null list entries and missing required
  members fail the whole response with a sanitized 503. No unknown stored fields
  are silently discarded or exposed. This is stricter than permissive JSON
  frameworks and is not asserted as original deserializer parity.
- Input JSON text above 1 MiB is rejected as a local candidate bound. PostgreSQL
  JSONB has already normalized duplicate object keys before the adapter reads it;
  the operation is not a raw JSON text preservation interface.

The handler accepts GET only and canonical hyphenated UUID text, including
uppercase hexadecimal. Other Guid textual formats and implicit HEAD behavior are
unimplemented local transport differences. Unknown query parameters are ignored.
Missing bot, config and data access errors never fabricate configured commands.

## Validation

`inventory/product-client-evidence/bot-command-oracle/` contains an independently
authored C# model fixture derived only from the eligible documentation. It ran
in-process with SDK 10.0.401/runtime 10.0.12. Nineteen cases cover null/empty lists,
nullable fields, required-field policy, casing, unknown members and wrong types;
six were accepted and thirteen rejected. Go compares decoded JSON semantics with
those saved results. Byte-for-byte escaping parity is not asserted.

This fixture is **not** the original shared-model, EF, Npgsql, live deployment,
or production authentication oracle. Its strict unmapped-member/nullable/required
options are explicit candidate choices. Replaying it must set
`DOTNET_GENERATE_ASPNET_CERTIFICATE=false`, diagnostics/telemetry off, an isolated
CLI home, no NuGet sources and no build servers. It neither starts a server nor
requires network access.

Race-enabled unit/recorder tests and source/fixture integrity assertions pass.
The real PostgreSQL tests are `TestPGDevelopBotCommandsPayloads`,
`TestPGDevelopBotCommandsPolicyAndErrors`, and
`TestPGDevelopBotCommandsRestartPersistence` (both before/after restart phases).
They cover actual configured JSON, missing-config fallback without writes,
inactive-bot behavior, candidate soft deletion, unknown-data errors, read-only
sessions, cancellation, closed connections and persistence. They compile but
have not run in this socket-restricted executor. CI must execute them without
skips; no database process/socket retry was attempted for this operation.

The fixture SQL is a minimal disposable projection, not a production migration.
Broader client research and precise remaining product gaps are recorded in
`docs/product-client-contract-audit.md`.
