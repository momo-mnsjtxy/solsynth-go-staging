# Apple Wallet member passes and callback service

This bounded backend implements signed Apple Wallet membership cards and their
registration/update web service. It is not Apple Pay and creates no payment,
admission, identity-verification or financial entitlement. The complete licensed
DysonNetwork controller, ApplePassService, options, authored models, account
download caller and implementation guide are retained with exact Git blobs and
SHA-256 hashes in `ports/passport/passkit/source/manifest.json`. Authored Solian
searches for `passbook`, `pkpass`, `AppleWallet` and `memberPass` returned no
caller; no SDK or generated-client implementation was inferred from that result.

## Routes and identity

- `GET/HEAD /api/accounts/me/passbook/member` requires current canonical
  first-party root-session proof and `accounts.manage`, and returns
  `solian-member.pkpass`. Scoped sessions without that permission, delegated
  children even with that permission, and app/OAuth sessions cannot provision
  or retrieve the enduring pass secret. This is an owned provisioning boundary,
  checked again inside the transaction before issuance and after signing/waits.
- `GET /api/passkit/v1` returns the service description when configured.
- `POST/DELETE /api/passkit/v1/devices/{device}/registrations/{type}/{serial}`
  authenticates the specific pass's `ApplePass` token. First registration returns
  201, repeated registration updates the push token and returns 200. Removal is
  idempotent. JSON accepts either `pushToken` or legacy `push_token`, not both.
- `GET /api/passkit/v1/devices/{device}/registrations/{type}` follows Apple's
  device-library-secret protocol. With an additional valid ApplePass header,
  the result is restricted to that token's pass. An invalid supplied header
  fails rather than falling back to device-only authentication. The response
  uses Apple's `serialNumbers` and `lastUpdated` names; no changes returns 204.
- `GET/HEAD /api/passkit/v1/passes/{type}/{serial}` authenticates the specific
  pass token and returns `member.pkpass`. A matching weak ETag returns 304 after
  current identity and content checks. Responses are private and no-store.
- `POST /api/passkit/v1/log` validates a bounded diagnostic batch and records
  its count and content digest through structured logging. Raw messages, which
  may contain credentials or private values, are not copied into server logs.

The existing `/passport/` and `/stargate/` gateway aliases are supported. Callback
headers are kept separate from bearer middleware. Pass secrets never authorize
account, media, wallet or other backend APIs. Account disable/punishment state
is checked again after waits and before results are published. Devices can only
list passes registered to their exact library identifier, which Apple's protocol
treats as a shared secret. Device identifiers must therefore be omitted from
public logs and should retain their client-generated high entropy.

## Signing, assets and configuration

The implementation uses only the Go standard library: ZIP, SHA-1 file manifests
required by Apple's format, SHA-256/RSA detached CMS signatures, DER encoding,
X.509 and PNG encoding. CMS includes the leaf and configured WWDR intermediate,
content-type/message-digest/signing-time attributes, and checks certificate
identity, key match, chain linkage and certificate validity. The operator supplies
the trusted Apple-issued chain; the program does not enroll certificates or
contact a provider. Independent OpenSSL tests verify the signature and chain and
reject modified manifests. These tests use locally generated disposable
certificates, not Apple production credentials. Actual Apple Wallet import on an
iPhone has not been tested and is not implied by cryptographic verification.

Signing is disabled by default. The command requires both
`--enable-apple-wallet` and `--apple-wallet-config=/absolute/path/config.json`.
The JSON file uses the fields below; file paths may be relative to that config.
No default certificate/key locations, environment secrets or credentials are
discovered. The existing RSA key must be a private regular file and use PKCS#1 or
PKCS#8 PEM; certificate inputs accept PEM or DER. P12 loading is not implemented.

```json
{
  "pass_type_identifier": "pass.example.member",
  "team_identifier": "YOURTEAMID",
  "organization_name": "Your organization",
  "description": "Membership card",
  "logo_text": "Member",
  "terms_text": "Your membership terms and contact information",
  "site_url": "https://example.com",
  "web_service_url": "https://api.example.com/passport/passkit",
  "signing_certificate_file": "member-certificate.pem",
  "signing_key_file": "member-key.pem",
  "wwdr_certificate_file": "AppleWWDR.cer",
  "assets": {"icon.png": "icon.png", "icon@2x.png": "icon@2x.png"}
}
```

Optional fields are `foreground_color`, `background_color`, `label_color`,
`barcode_alt_text`, `associated_store_identifier` and `program_group`.
`program_group` is an explicitly selected canonical commerce subscription group;
only a currently active subscription in that group supplies the program label.
No excluded perk enum or arbitrary third-party subscription is assumed to be the
member program. The mapping is enforced for first-party/internal definitions
only. App-bound definitions, app-product subscriptions and testing subscriptions
are excluded, including when the selected group contains only those records;
the card then says Standard. Internal current-cycle selection follows the
canonical status/testing/time checks, definition-owned group and begun-at/ID
ordering. Full app-product eligibility needs its ancestry/product/consent lock
protocol and is not appended after pass/media locks. Without that mapping the card says Standard, matching the source
service's absent-perk fallback. The selected subscription is read again before
publication so expiration during signing cannot publish stale entitlement text.

The configured URL is the base *before* `/v1`: Wallet appends the protocol
version. A trailing `/v1` is rejected to prevent the duplicated-version URL in
the retained source example. Both URLs must be HTTPS. All configuration values
above are examples; no production key, certificate, account or endpoint was used.

Required static icon PNGs are bounded and validated. Optional logo/thumbnail
variants use a fixed filename allowlist. A current owner-controlled canonical
profile image is preferred, with digest/size checks, row locks and bounded
decoding. Supported PNG/JPEG/GIF profile images are converted to PNG icon and
thumbnail variants. Other formats, deleted/missing files and failed object reads
use configured fallback assets. There is no external profile-image HTTP fetch.
Cards contain the source-visible username, display name, membership year,
program label, terms, profile URL, account UUID and profile QR code. No birth
date, contact list, password, account bearer token or signing private key is included.
Configured PNGs must decode completely; a valid header on a truncated image is
rejected before it can become a signed asset.

## Persistence and differences from the retained source

Apply `internal/passkitpg/schema.sql` explicitly after the canonical account,
profile/media and commerce schemas. Constructors and readiness never migrate.
One pass exists per account and pass type, with the account UUID as serial and a
stable random 24-byte authentication token. Concurrent issuance shares the same
canonical account/pass locks, token and record. Token storage remains private to
the service because the token must be included when the pass is regenerated.
Signing failure or expired account proof rolls back initial issuance and update
metadata; it does not leave a successful-looking pass record.
The pass token is an independent credential after authorized provisioning:
ordinary bearer-session revocation does not revoke a Wallet pass. Device
unregistration removes only that registration. Account disable or an active
disable punishment denies callbacks. No explicit pass-token revocation or
rotation endpoint is implemented in this bounded slice.

Updates use a durable sequence and a SHA-256 fingerprint of actual source files
and the signer chain. This fixes the source's lexicographic ordering of content
hashes, notices certificate rotation and observes current profile changes even
before a separate download. Tokens and serials remain stable. A content change
during signing fails without publishing stale bytes. Weak ETags describe semantic
content; the detached signature's signing time can change on regeneration.

Multi-pass device lists lock canonical account IDs in sorted order and re-read
current registrations before returning. They do not use the source's first-pass
token to authorize all the remaining passes. Apple-standard device-only queries
are supported independently, as specified by the actual Wallet protocol. The
list is bounded to 1,000 registrations per request; excess returns an explicit
availability error rather than a partial success.

APNs push delivery is not part of this slice and is also explicitly absent from
the complete upstream service. Push tokens are stored for that future adapter;
no push is sent or claimed. Personalization/reward enrollment, legacy table
migration, P12 decryption and real-device installation remain separate work.

## Verification and primary contracts

Tests check genuine signed archives, manifest consistency, OpenSSL CMS/chain
verification and tampering rejection, signer identity/expiry, command opt-in and
key privacy, bearer/callback separation, device queries, malformed registration
bodies, signed profile projections, concurrency, rollback, authorization/media
waits and restart state. The restart marker contains only disposable synthetic
signing material and fixture state in the private disposable cluster directory;
the after phase reloads it and does not seed new account/pass/registration state.
Ordinary package success skips PostgreSQL without its guarded test DSN and must
not be reported as actual database validation.

- [Apple pass construction and signing](https://developer.apple.com/library/archive/documentation/UserExperience/Conceptual/PassKit_PG/Creating.html)
- [Apple update service and authentication](https://developer.apple.com/documentation/walletpasses/adding-a-web-service-to-update-passes)
- [Apple registration status codes](https://developer.apple.com/documentation/walletpasses/register-a-pass-for-update-notifications)
- [Apple serial-list protocol](https://developer.apple.com/documentation/walletpasses/get-the-list-of-updatable-passes)
- [Apple serial-list JSON](https://developer.apple.com/documentation/walletpasses/serialnumbers)
- [Apple web-service base and version](https://developer.apple.com/library/archive/documentation/UserExperience/Conceptual/PassKit_PG/PassPersonalization.html)
- [IETF CMS, RFC 5652](https://datatracker.ietf.org/doc/html/rfc5652)

No third-party implementation dependency was added. The RFC and Apple links are
protocol references; no Apple SDK, excluded Shared/NeTo/Spec definition,
generated binding, or unlicensed implementation was copied.
