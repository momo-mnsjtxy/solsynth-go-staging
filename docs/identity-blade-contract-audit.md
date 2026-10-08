# Blade identity and gateway contract audit

Audit date: 2026-10-08. Repository: `Solsynth/Blade`. Immutable revision: `dbded8c29f1cba55dd67086c786ac4e7432096ca`.

## Result and evidence boundary

The owned gateway, WebSocket session lifecycle, and revocation consumer behavior are established by static authored-source evidence. End-to-end identity compatibility is **partial**: Blade delegates token extraction, authentication RPC construction, caching, metadata cloning, and profile hydration to the excluded SoSYS Go dependency. Its callers and README cannot establish the dependency's implementation, generated wire contract, or authority-side behavior.

All 46 Go files in the complete, untruncated pinned tree were retained and searched for identity, credential, session, authorization, and forwarding usage. Relevant execution paths, adjacent configuration, and upstream tests were inspected. The original local inspection covered 58 exact upstream files totaling 386,133 bytes. The publication-safe evidence set retains 53 exact files (including all 46 owned Go files), while five deployment configurations are omitted; their pinned URLs/hashes remain metadata only. It is not a buildable whole-repository export.

- Evidence root: `inventory/identity-client-evidence/blade/`
- Exact file inventory, pinned Git blob SHA, SHA-256, size, and source URL: `inventory/identity-client-evidence/blade/manifest.json`
- Complete tree metadata: `inventory/identity-client-evidence/blade/tree.json`
- Retention verification: `inventory/identity-client-evidence/blade/verification.json`
- No upstream tests or services were executed. No live backend, account, or credential was used. No dependency, submodule, generated binding, descriptor, schema, or `.proto` content was retrieved.

The pinned [LICENSE.txt](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/LICENSE.txt) contains the GNU Affero General Public License, Version 3. Its Git blob is `be3f7b28e564e7dd05eaf59d64adba1a4065ac0e`, byte count is 34,523, and SHA-256 is `0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`. The license was read and its identity established before authored source retention. This is repository license evidence; it does not establish dependency rights or an additional version-or-later grant.

Stargate was not accessed because that research had been canceled. SoSYS and Socommon were not accessed or retried: their permission boundary remains unresolved within the canceled scope. This audit makes no claim that those sources lack licenses or are technically unavailable. NeTo/Spec definitions and generated protocol artifacts are explicitly excluded. Other missing-license repositories were not accessed. These boundaries are not negative findings about any excluded implementation.

## 1. HTTP gateway routes and forwarded credentials

Evidence: [internal/proxy/proxy.go](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/proxy/proxy.go), particularly lines 62–149, 197–268, and 270–327; `internal/proxy/proxy_test.go:43–74,82–111,145–182`; `cmd/main.go:159–160,267–268,333`.

- Default service routing maps `/{service}` to the service's `/api`, and `/{service}/{remainder}` to `/api/{remainder}`. The service prefix is removed. `/swagger/{service}/{remainder}` becomes `/swagger/{remainder}`.
- Configured special routes run before default service routing inside the fallback proxy. Prefix routes preserve the suffix after the configured path. Non-prefix rules match either the exact path or a slash-delimited descendant, but send the configured target path without preserving that descendant suffix.
- The director rewrites scheme, host, path, raw path, and request Host. Incoming query data is retained; any target query precedes it. The authored test for a fallback `/ws` route explicitly expects the `tk` query to remain present at the upstream.
- The authored proxy does not extract, validate, delete, replace, or convert Authorization or Cookie credentials, and does not inject authenticated account/session headers. Existing headers are passed to `net/http/httputil.ReverseProxy` without Blade-owned credential transformation. This supports HTTP credential pass-through as the owned behavior; exact standard-library hop-by-hop/header processing was not separately audited or runtime-tested.
- HTTP proxying is not the WebSocket authentication path. When the built-in WebSocket gateway is enabled, its configured GET route, normally `/ws`, is registered explicitly before the `NoRoute` fallback. A configured fallback `/ws` rule is not evidence that the built-in route forwards to an identity service.
- Service discovery can add routes not listed statically. Registered instances take precedence. When registered instances exist but none can provide a healthy HTTP endpoint, the code does not fall back to the static endpoint; no instances permits static fallback.
- The global readiness middleware precedes the built-in WebSocket route and fallback proxy. Health endpoints and relay registration routes are mounted before that gate. Maintenance mode lives in the fallback proxy, rather than a global authentication layer.

Identity discovery routes are configuration-dependent. `internal/config/config.go:147–154` defaults the OIDC configuration and JWKS paths to service `pass` with `/auth/...` targets. The committed development configuration instead targets service `stargate` and unchanged `/.well-known/...` paths, including permissions and error-code documents (`configs/config.toml:83–111`). Those are owned routing references only; no named authority repository was opened.

## 2. WebSocket credential extraction and authority projection

Evidence: [internal/wsgateway/http.go](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/wsgateway/http.go), lines 38–101; [cmd/main.go](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/cmd/main.go), lines 162–217,249–268; `README.md:295–301`; `go.mod:16`.

Confirmed at the authored caller:

1. Read the optional `namespace` query, falling back to configuration.
2. Call `dyauth.AuthenticateRequest(request.Context(), authenticator, request)`; an error produces HTTP 401 and no upgrade.
3. Independently call `dyauth.ExtractToken(request)` and retain its `TokenInfo`; this second call's error is ignored.
4. Best-effort `dyauth.HydrateAndTouch` uses the cache, profile client, and authentication result.
5. Derive the device ID from `auth.Session.GetClientId()`. Pass Account, Session, TokenInfo, and `dyauth.CloneRequestMetadata(request)` into the connection service. A blank device ID later receives a generated UUID. No authored Go caller reads `deviceAlt`; whether an excluded helper interprets that query parameter is unproven.

`cmd/main.go` selects the configured auth service's static gRPC endpoint and constructs `dyauth.NewGrpcTokenAuthenticator` with Target, UseTLS, TLSSkipVerify, and TLSServerName. It wraps that authenticator with `NewCachedTokenAuthenticator`, backed by Redis when configured or an in-memory cache otherwise. The profile client is constructed separately. This is a dependency boundary, not an authored `Authenticate` request projection.

README-only assertions state extraction precedence of `tk` query, then Authorization with Bearer/AtField/AkField, then AuthToken cookie; a remote `DyAuthService/Authenticate` call; and forwarding of the request IP as `ip_address`. The caller is consistent with delegating those jobs, but does not implement or prove their exact precedence, case handling, empty-value behavior, token-type mapping, forwarded-IP trust, metadata fields, cache key/TTL, RPC request fields, or return/error mapping.

The dependency declaration is `src.solsynth.dev/sosys/go v0.2.1-0.20260919143325-40fa743b4bd0`. Its source and generated `proto` package were not acquired. Authored imports, getters, and test constructions do not prove protobuf field numbers, service names on the wire, generated descriptors, or cross-language compatibility. No authority authentication or JWT validation implementation is established by this Blade-only audit.

## 3. Session expiry and reauthentication

Evidence: [internal/wsgateway/service.go](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/wsgateway/service.go), lines 185–218,321–369,396–460,913–946,1178–1233; `internal/wsgateway/service_test.go:231–347`.

- Connection identity is keyed by namespace, account ID, and device ID. A replacement connection closes the previous connection at that key.
- Expiry is taken from the returned session's `ExpiredAt`, not parsed from a JWT by owned code. An unset or Unix-epoch-or-earlier value is treated as no scheduled expiry.
- An empty token or an API-key token disables session reauthentication. API-key classification uses either `TokenTypeApiKey` or the external `IsApiKeyToken` helper. `TryAdd` explicitly clears the stored expiry when reauthentication is disabled. The upstream API-key test documents that behavior; it does not establish how the dependency classifies arbitrary tokens.
- Other connections with a nonzero expiry wait until that expiry, then call external `Reauthenticate` using the original TokenInfo and cloned request metadata. A past expiry schedules an immediate check. There is no Blade-owned earlier periodic validation interval.
- The returned result must include Account and Session, preserve the account ID, and normally have a future or unset expiry. A returned API-key session type is exempted from the already-expired check. Success refreshes stored account/session ID/expiry and best-effort profile hydration; failure disconnects the connection with an error reason.
- Initial validation, dependency cache freshness, bypass of cache during reauthentication, and behavior when the authority revokes a token remain unproven. The owned code's name `Reauthenticate` is not evidence of a fresh authority request.

The WebSocket receive loop captures the initial Account object for packet handling. The metadata updater changes the connection entry, but this does not establish refresh of any permissions in downstream packet handlers. No owned permission evaluator was found in this path.

## 4. Revocation consumption

Evidence: [internal/wsgateway/revocation_nats.go](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/wsgateway/revocation_nats.go), lines 12–75; `internal/wsgateway/service.go:280–318`; `cmd/main.go:227–266`; `internal/wsgateway/revocation_nats_test.go:11–43`; `internal/wsgateway/service_test.go:183–228`.

- If NATS is configured while the WebSocket gateway is enabled, each gateway subscribes to the fixed `auth.session.revoked` subject. This subject is not derived from `websocketSubjectPrefix`.
- The authored consumer decodes JSON fields `event_id`, `event_type`, `session_id`, `account_id`, `client_id`, and `device_id`. The preferred device identifier is the first nonempty trimmed device ID, then client ID. Event ID/type are logging metadata in this consumer.
- It calls `DisconnectSession` with an empty namespace. That resolves to the service's configured default namespace, rather than all namespaces. Other namespaces can be used by the HTTP handler, so do not describe this consumer as a cross-namespace revocation broadcast.
- Within that namespace, a nonempty matching session ID selects the connection. Only when the event has no session ID does matching fall back to account ID plus a nonempty device identifier, accepting exact device ID or the device ID plus a `+` suffix.
- A nonmatching nonempty session ID does not activate account/device fallback. Account-only events do not select all account connections. Selected entries are removed and closed with the session-logout reason.
- This owned handler does not explicitly invalidate the auth cache. Producer-side publication, reliable delivery/replay, dependency cache invalidation, and successful denial of a reconnect are not established. An ordinary NATS subscription is visible; no additional durable-consumer or replay setup is authored here.

These are consumer expectations for integration and lifecycle testing, not evidence that the excluded authority emits matching events.

## 5. Permission usage and forwarded identity

Evidence: `internal/wsgateway/service.go:22–33,888–910`; [internal/wsgateway/forwarder_nats.go](https://github.com/Solsynth/Blade/blob/dbded8c29f1cba55dd67086c786ac4e7432096ca/internal/wsgateway/forwarder_nats.go), lines 24–40,59–104,155–158; `internal/wsgateway/grpc.go`; `internal/capabilities/aggregator.go`.

- Owned HTTP proxy and WebSocket packet dispatch code do not apply a role, claim, permission, or scope check. Authentication and authorization must not be conflated.
- Packet handlers receive an Account and device ID. The default main program supplies no local packet handlers. Unknown packets with a nonempty endpoint are sent to the configured forwarder.
- NATS forwarding projects the authenticated account ID and device ID alongside event metadata and packet bytes. It does not forward the original token, session ID, permission set, or role set. The endpoint is normalized for the subject by trimming, lowercasing, and removing a leading `dysonnetwork.`; the original endpoint remains in the authored JSON packet.
- The gRPC WebSocket service provides push, connection-status, device-list, and packet-receive operations. Its authored methods validate required fields but do not invoke the WebSocket token authenticator or permission service. This is evidence of an internal-service trust boundary, not a conclusion about public reachability or production exposure.
- `/meta` capabilities describe service/API features and revisions. They are not account permission decisions. A configured `/.well-known/permissions` route likewise only proxies a document.

## 6. Deployment trust and configuration

Evidence: `cmd/main.go:82–97,118–128,162–217,269–319,364–392`; `internal/config/config.go:68–89,117–154`; `internal/discovery/credential.go:8–22`; `internal/discovery/grpc.go:28–109`; `internal/discovery/relay_http.go:151–162`; `internal/relay/registration.go:52–65,149–157`; `internal/capabilities/aggregator.go:172–191`; `docs/SERVICE_DISCOVERY_CLIENT.md:29–31`.

- Auth target and TLS options are deployment inputs. Defaults use service `pass` and no auth TLS. The committed development example selects another authority service and enables TLS with certificate verification skipped. These are examples, not verified production settings.
- Profile target/TLS are separate inputs. A `ProfileTLSServerName` field exists, but the shown profile TLS constructor does not use it. The auth constructor does receive its corresponding server-name setting.
- The development file lists `passport` but does not provide the default `pass` profile endpoint. The main program requires a nonempty profile target when WebSockets are enabled. A deployment must supply consistent service/profile settings; the example alone is not evidence of a runnable identity integration.
- The main HTTP listener uses `ListenAndServe`, and the main gRPC server is constructed without authored transport credentials or authentication interceptors. Ingress TLS and internal-service isolation therefore require deployment evidence outside this source path. CORS allows all origins with credentials. The WebSocket handshake delegates Origin parsing and does not implement an origin allowlist.
- Service registration is a separate credential boundary: mutating discovery RPCs and relay registration HTTP routes compare incoming authorization to the configured registration token. Empty configuration cannot authorize registration. Read-only discovery resolution is separate from that check. This credential is not an end-user token or permission grant.
- Registry endpoint values affect routing. Healthy registered instances override static endpoints. A configured registry and broker belong to the deployment's trusted service infrastructure; actual network policies, credentials, and access controls were not inspected.
- NATS setup is delegated to the external eventbus helper using the configured URL. Broker authentication, TLS policy, publisher authorization, and reconnect guarantees beyond the authored options are outside the proved contract.
- The optional L4 relay chooses an allowlisted upstream from TLS SNI and forwards bytes without terminating TLS (`internal/relay/server.go:208–290`). It does not implement HTTP credential conversion. Relay registration uses a separate configured HTTP(S) endpoint and bearer registration token, with optional TLS verification skipping.
- Capability RPC clients default to TLS, allow a configured verification skip, and select plaintext for an explicit `http` endpoint. This is separate from WebSocket auth TLS configuration.

Sensitivity note: upstream configuration contains credential-shaped sample values. They were never used or tested. After publication review, all five deployment configuration snapshots were omitted from the distributable tree. Their metadata is retained, not their values; all references to those files describe prior local inspection. No such value is reproduced in this report. Do not deploy upstream examples as secrets.

## 7. Coverage and completion criteria

**Confirmed statically:** owned route rewriting; absence of Blade-owned HTTP credential conversion; built-in WebSocket auth helper call boundary; account/session/device projection; local expiry monitor rules; default-namespace revocation matching; NATS forwarded identity shape; owned permission-check absence; configurable trust boundaries.

**Partial/documented only:** token extraction precedence, auth RPC name and request IP projection, auth cache behavior, token classification, metadata cloning, profile hydration, and authority calls. README assertions and call sites must remain labeled as such.

**Not established in this evidence:** authority-side verification and revocation producer, SoSYS helper implementation, actual deployment, and production network policy. These were not inspected and are currently outside authorized research. In particular, SoSYS/Socommon are not accessed with the permission boundary unresolved, rather than proved unavailable or unlicensed. They cannot be promoted to compatible or incompatible based on this audit.

**Explicitly excluded:** NeTo/Spec definitions, `.proto` content, descriptors, and generated protocol bindings. Authored caller references do not substitute for those definitions. README authentication names remain documented partial facts, as described above.

**Unexecuted:** all upstream Go tests and runtime integration. Tests were retained and read as authored evidence. They cover route/query rewriting, discovery precedence, expiry refresh, epoch sentinel, API-key monitor suppression, session/device matching, and one NATS revocation-consumer case. They do not establish token extraction, end-to-end authority calls, HTTP header/cookie forwarding, reconnect denial after revocation, or cross-namespace delivery.

Remaining identity work should use an explicitly supplied and authorized authority/adapter contract or an independently specified local boundary. Acceptance should separately establish extraction precedence, trusted IP derivation, cache freshness and revocation invalidation, namespace coverage, internal-service authentication, and message compatibility. No existing result here justifies claiming full wire compatibility or a production-ready identity integration.
