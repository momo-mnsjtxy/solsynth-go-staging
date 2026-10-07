# Passport username utility port

## Source and license

- Repository: https://github.com/Solsynth/DysonNetwork
- Revision: 4ee2d0cebb9da8284a92483fee6dce795bf6947c
- Source: https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Account/AccountUsernameService.cs
- License: GNU Affero General Public License v3.0, copied verbatim to LICENSE.txt
- Original source retained in source/AccountUsernameService.cs for review

This is a narrow Go utility translation, not a running identity service or a
completed platform migration. It does not modify upstream repositories, accounts,
databases, auth, tokens, or network configurations.

## Preserved behavior

The source strips non-ASCII username characters (despite its comment mentioning
replacement with underscores), lowercases, prefixes a non-letter start with u,
then truncates to 30 ASCII bytes. Empty sanitized names fall back to user.
Generation searches the base first, then tries ten random integers from 1000
through 9998 inclusive. A final Unix-millisecond suffix is returned without an
availability check. Suffixes are not truncated to 30 characters. Search errors
terminate generation. Email names use the portion before the first @.

The adapter receives the supplied context and the original query. It must
preserve the existing account-search RPC's filtering, pagination, and error
semantics. No database uniqueness reservation or transactional guarantee is
added. The upstream generator is not a guarantee against concurrent collisions.

## Deliberate boundaries

- Search transport, protobuf integration, HTTP routes, persistence, account
  creation, and all existing Stargate authentication behavior remain untouched
- Random output sequences differ between .NET Random and Go math/rand; bounds,
  attempt counts, and deterministic injected fixtures preserve the algorithm
- Go strings represent upstream null input as an empty string
- Generated and valid stored username names are ASCII. Arbitrary Unicode
  comparisons via Exists are not verified against .NET OrdinalIgnoreCase;
  Go strings.EqualFold is used and Unicode edge cases may differ
- Nil search dependency is reported explicitly as ErrSearchUnavailable
- Synthetic tests check source-derived expectations; the C# source has not been
  executed for differential tests because no .NET toolchain is available

## Identity platform context

Stargate is already Go (module src.solsynth.dev/sosys/stargate), with PostgreSQL,
GORM/pgx, Gin, gRPC, Redis, NATS, JWT and WebAuthn dependencies. Its current source
describes replacement of Padlock plus the Passport accounts/profile/social graph.
It preserves /api routes beneath Blade gateway prefixes. JSON conventions include
snake_case, integer enums, UTC RFC3339 times and omitted nulls, with traceId an
exception. Existing identity contracts, schemas and migrations must remain the
authority; this utility does not replace them.

Remaining C# Passport at the source revision targets net10.0. It retains board,
presence, leveling, realms, progression, tickets, meetings, nearby, NFC, domain
trust and other services. Its account RPC client already targets _grpc.stargate.
The existing Stargate migration retains UUIDs, password hashes and E2EE data;
this work neither runs nor changes that migration.

Evidence:
- https://github.com/Solsynth/Stargate/blob/01dfcdd7e633ca7abc82306684ae4b8dd37dd2f0/README.md
- https://github.com/Solsynth/Stargate/blob/01dfcdd7e633ca7abc82306684ae4b8dd37dd2f0/go.mod
- https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/DysonNetwork.Passport.csproj
- https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Startup/ServiceCollectionExtensions.cs
