# Owned location and media completion

This continuation implements the owned PostgreSQL contracts below. It does not
translate old file identifiers, imported records, legacy payloads or storage
configuration. The source references are `ports/media/source/DRIVE_API.md`, the
retained location services and `ports/sphere/locations/source/PERMISSIONS.md`.

| Area | Implemented behavior | Configuration or client requirement |
|---|---|---|
| Nearby | Rotating random 128-bit tokens, hash-only storage, 60-second slots, authenticated session binding, opt-out revocation, mutual-friend default, bilateral blocks, token/session expiry, observation bounds and five-second account limits using the post-lock database clock | Client advertises the returned service UUID/token and submits bounded observations; this server does not collect device coordinates |
| Encrypted NFC | Wrapped AES keys, explicit administrative enrollment, verified SUN MAC and monotonic read counter, physical-proof claim, current owner/block checks, owner deletion, tombstone restoration with retained replay counter, and authenticated physical lock readback | Stable injected 32-byte wrapping key; enrolled physical tag must use the documented MAC profile; physical locking uses the explicit Linux PC/SC provider below |
| Meets | Owned ready passive-image attachment, canonical parent audience, private bundle/expiry restrictions | Media storage must be configured; read access does not grant attachment ownership |
| Location streams | Durable audit sequence replay, resource-bound `Last-Event-ID`, current-audience rechecks and fresh state projections | Cursor is `<resource UUID>:<sequence>`; historical coordinates are never replayed |
| Geocoding | Authenticated, bounded and rate-limited lookup, configured HTTPS adapter, public-DNS pinning, strict bounded response and original-proof recheck after provider wait | Explicit owned provider endpoint; no device acquisition or implicit geocoding of stored private locations |
| Upload management | Task statistics, recent tasks, owned task details with pool/bundle metadata, uploaded bytes/speed estimate and durable failed/expired cleanup | Quota, storage and owned upload schema must exist; estimates use persisted activity, not a claimed live network speed |
| Files | Owned folders, cycle prevention, names, metadata, sensitivity labels, indexed/unindexed and hierarchy-independent listing, atomic batch authorization, deletion retry and references | Parent and pool IDs use owned UUIDs; nonempty folders cannot be deleted |
| Deduplication | Verified same-owner/same-pool byte sharing, independent logical ACLs/quota charges and last-reference deletion | No cross-account hash existence oracle or hash-only upload bypass |
| Remote uploads | Explicit HTTPS-host allowlist, redirect validation, public address pinning, eight active fetches, response/time/size bounds and normal verified upload commit | Empty allowlist disables outbound fetching; no ambient proxy or default unrestricted client |
| Thumbnails | Bounded PNG/JPEG/GIF/WebP decode, at most 16 million source pixels and 256-pixel output; AVIF uses the configured bounded renderer; audience/proof rechecked after processing | AVIF requires explicit Linux `ffmpeg` and `prlimit`; generated previews do not create a separately charged file |
| Live photos | Atomic, idempotent association of an owned ready image and video, real bounded image/video validation, shared pool/bundle policy and both component audiences | `POST /api/files/compositions/live-photo` with `image_id`/`video_id`; supplied `application_type` alone is insufficient to define a packaged file format |
| E2EE | Opaque ciphertext plus validated bounded header/signature metadata, no server thumbnail or plaintext interpretation | `file.aesgcm.v1`, octet-stream data and a client implementing encryption/signature verification; storing bytes is not client cryptography |
| Expiry and quotas | Ready-file/bundle expiry denies reads before cleanup; durable physical deletion releases the correct charge once; explicit expiring quota records and idempotent grants | Owned quota records use bytes; administrator grant permission is required; no payment channel or fabricated price is implemented |

The executable supports `--media-pools-config`, `--media-remote-hosts`,
`--nfc-key-encryption-env`, `--media-renderer-ffmpeg`,
`--media-renderer-limiter`, `--location-geocoder-endpoint`, and the three
physical-provider flags described below. Pool configuration
is an absolute regular mode-0600 JSON file, with a version and one to 100 entries:

```json
{"version":1,"pools":[{"id":"d5100000-0000-4000-8000-000000009999","root":"/srv/solsynth/pool-a"}]}
```

Pool roots must already be private mode-0700 directories. Opening storage does
not seed pool policy rows; guarded `POST /api/pools` enrollment is separate.
Custom `media.ObjectStore` implementations can provide separately configured
remote storage; the executable's pool provider is the pinned filesystem store.

The owned geocoding adapter sends an encoded `q` and optional `language` to its
configured HTTPS endpoint. The response is a `places` array of at most ten
objects with `name`, `address`, required finite `longitude` and `latitude`.
The request is explicit user input, has a five-second provider deadline and
does not persist a new location history.

The Linux raster adapter executes only operator-provided absolute executables,
fixed arguments and a pipe-only input protocol. It caps active processing at
two jobs, input at 20 MiB, output at 1 MiB, process address space at 512 MiB,
CPU at four seconds and wall time at five seconds. It extracts a single frame
and does not fetch external media or interpret uploaded values as arguments.
The pinned WebP decoder is `golang.org/x/image v0.46.0`; it requires Go 1.26.

`mediapg.FileReadProtection` is a mandatory restriction distinct from optional
parent visibility grants. Domain-bound files must receive the canonical
provider through `WithFileReadProtection`; location projections receive the
same provider through `WithMediaProtection`. It applies even to owners/public
files, and runs after query rows are closed and after processing waits. A
forbidden protected item is omitted before visible counts and pagination. Quota/statistical aggregates
still describe the owner's charged objects, including durable deletion retries.

Bundle secrets are supplied only in a single `X-Bundle-Passcode` header.
`?passcode=...` is rejected for files, thumbnails, compositions and bundles;
clients must keep secrets out of URLs, browser history and query logs. Bundle
and quota expiry are checked with the database clock after domain-row waits.
Owner projections consistently exclude current scheduled `disable_account`
punishments; `block_login` remains an original authentication restriction.
Ordinary, remote and resumable upload completion reload the persisted options
after object/deduplication waits and again after state writes, before the final
original-proof check. A newly disabled public pool owner cannot turn a retained
reservation into a ready file. Resumable policy failures remain durable and
release their reservation through the same physical deletion retry contract.

Physical NFC locking has a concrete Linux PC/SC adapter. Build and install the
owned transport helper from `scripts/nfc-pcsc-bridge.c`:

```bash
cc -std=c11 -O2 -Wall -Wextra -Werror -o nfc-pcsc-bridge scripts/nfc-pcsc-bridge.c -ldl
```

It requires system `libpcsclite.so.1`, a running `pcscd`, an explicitly named
ISO14443-4/T=1 reader, and the enrolled NTAG424 DNA presented to that reader.
Configure `--nfc-pcsc-helper`, `--nfc-pcsc-limiter` (absolute trusted regular
executables), `--nfc-pcsc-config` (absolute private mode-0600 strict JSON), and
`--nfc-key-encryption-env` (stable base64 32-byte wrapping key). The binding
file has this format; it stores environment names rather than application keys:

```json
{"version":1,"tags":[{"record_id":"11111111-1111-4111-8111-111111111111","uid":"04958CAA5C5E80","reader":"Exact enrolled reader name","application_key_env":"ENROLLED_TAG_APPLICATION_KEY","key_number":0,"ndef_uri_template":"https://nfc.example/sun?record_id=11111111-1111-4111-8111-111111111111&picc=00000000000000000000000000000000&cmac=0000000000000000"}]}
```

Each application-key environment value is strict base64 for an independently
personalized, nonzero 16-byte application change key. It is distinct from the
SDM metadata/file-read keys recorded by administrative enrollment. Duplicate
UID/record bindings, missing keys and partial configuration fail closed.
The independently enrolled NDEF must contain one short URI record with the
`https://` prefix code 04 and exactly the configured site, path and ordered
query: `record_id`, `picc`, optional `enc`, then `cmac`. The template contains
32 zeroes for each PICC/encrypted mirror and 16 for CMAC; `encrypted-file`
adds `&enc=` and its 32 zeroes immediately before `&cmac=`. Actual NDEF bytes,
static record ID and all mirror offsets are verified before physical writes.
This is an explicit owned provisioning contract, not a guessed legacy URI.
`Ready` verifies the actual configured PC/SC reader service. Without explicit
configuration the physical-lock API remains unavailable.

The adapter supports the current AES NTAG424 SDM encrypted-PICC profiles `empty`
and `encrypted-file`, with UID and read-counter mirrors, 256-byte NDEF, freely
readable SDM output, and no read-counter-limit extension. It rejects other
settings before changing hardware. It reads a fresh SUN and verifies it against
the canonical record's wrapped SDM keys and monotonic counter, authenticates
EV2First with a fresh random challenge, and verifies the secure GetCardUID and
MAC-protected GetFileSettings responses. ChangeFileSettings uses full encrypted
secure messaging and preserves SDM settings, while changing NDEF ReadWrite,
Write and Change rights to F (disabled). A subsequent MAC-protected settings
readback must confirm that state. This protects physical NDEF bytes and their
settings; it does not invent legacy decoding or claim to personalize factory
cards or change the separate capability-container contents.

The helper has one exclusive card connection, at most 16 APDUs of 261 request
and 258 response bytes, no shell or network, and powers down on normal close.
The parent allows one operation, 64 MiB address space, two CPU seconds and an
eight-second wall deadline. Application keys remain in the Go process; the
helper sees authenticated APDUs. The database stores a verified SUN counter and
transcript digest only after the original request proof passes the final check.
A physical write cannot be rolled back: a lost receipt or failed SQL commit
leaves the database unconfirmed; a retry/restart authenticates the already
locked settings rather than blindly reissuing an impossible write.

Cryptography is checked against NIST CMAC and NXP AN12196 rev2 sections 4/5
EV2 authentication, MACed-read and encrypted ChangeFileSettings vectors;
section 5.7.3 defines the short URI NDEF header used by this provisioning contract:
https://www.nxp.com/docs/en/application-note/AN12196.pdf.
File rights, APDUs, IVs and padding follow the NT4H2421Gx datasheet:
https://www.nxp.com/docs/en/data-sheet/NT4H2421Gx.pdf.
The PC/SC ABI is documented at https://pcsclite.apdu.fr/api/group__API.html.

Local evidence consists of package tests, vet, race tests, SQL argument controls
and PostgreSQL parser controls. The AVIF test executed the explicitly available
Linux renderer on a synthetic fixture; no physical NFC device or live external
geocoder was used. Twenty-four new PostgreSQL cases cover nearby privacy/rotation,
NFC claim/replay/locking contracts, image ownership, durable location replay,
geocoding proof expiry, hierarchy/dedup/batch isolation, encrypted expiry/pools/
remote/thumbnail contracts, task cleanup/bundle protection, quota expiry,
owner-scoped deletion retry and live-photo composition. The review cases add
actual SQL-lock waits across TTL/proof expiry, dual-instance clock skew, sorted
assignment races, protected pagination, scheduled owner/recipient disable,
uncertain hardware-commit recovery and concurrent quota conservation.
The final review adds ordinary/resumable pool-owner disable during an observed
database wait after object I/O, SUN owner blocking during an audit wait, and
plain NFC row/audit waits across original session expiry. PC/SC card-emulator
tests require zero physical writes for wrong records, sites, paths, URI types
and mirror offsets for both supported MAC profiles. PostgreSQL cases skip
without the authorized CI database. Parser success and package `ok` therefore
do not claim actual PostgreSQL, restart or production-provider validation.
