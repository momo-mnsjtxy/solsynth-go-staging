# OAuth device authorization and scoped resource access

These routes use the configured canonical HTTPS issuer. OAuth remains opt-in in
the unified backend, and the existing authenticated consent UI URL remains
required. The consent client must show the device client name and requested
scopes before submitting a decision.

- `POST /auth/open/device`: form client credentials and requested scopes; returns
  one-use device code, human user code, verification URI, expiry and poll interval.
- `GET /auth/open/device/verify?user_code=...`: authenticated ordinary user preview.
  `POST` to the same route submits form `user_code` and `authorize=true|false`.
  Decision requires the same user/session's recent preview. Device codes and user
  codes are hashed in PostgreSQL; pending polls, slowdown and single-use redemption
  are durable across instances. Explicit app device grant enablement is required.
- `POST /auth/open/token` accepts the RFC 8628 device grant alongside existing
  PKCE authorization code and refresh grants. The issued session binds the parent,
  app version, publisher owner, client credential and versioned consent. Refresh
  does not extend the original grant or parent lifetime.
- `POST /auth/open/introspect`: confidential owning client only; inactive responses
  contain only `active:false`. Signed access-token expiry is checked independently
  from session expiry. Public clients and ordinary user/ID tokens cannot introspect.
- `POST /auth/open/register`: authenticated owner registration using canonical
  developer/project coordinates. JSON includes client name, redirect URIs, grants,
  response type `code`, scopes and token endpoint authentication method. Existing
  `developers.apps.create` and (for confidential clients)
  `developers.apps.secrets.manage` permissions apply. Client and initial secret
  commit atomically; the secret is returned once and expires after 24 hours.
  Existing developer management provides rotation and revocation.

Resource tokens enter the unified host only through the explicit route/method
scope map in `internal/application/oauth_resources.go`. Current account/session,
consent, app and domain visibility checks still apply. Post create/update/delete
and chat message create/update/delete use the same exact permission scope as
their ordinary counterpart. Read scopes cover posts, profile, chat, wallets,
notifications and realms. A read scope does not satisfy additional sensitive
domain permissions. Administration, credentials, account mutations and wallet
mutations are outside this delegation map, even for a superuser subject.

Device verification UI and clients must adapt to these new contracts. Real
PostgreSQL, concurrent redemption and restart checks are mandatory CI evidence;
ordinary unit tests or fixture declarations do not establish that evidence.
