# Media source boundary

This is an independently authored local-filesystem lifecycle informed by the
licensed Drive API documentation. It is not a translation of the removed Drive
implementation or a claim of full existing-client parity.

The retained AGPL-3.0 sources and licenses are pinned and hashed in
`source/manifest.json`. DysonNetwork is pinned to
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`; Solian is pinned to
`ffd0838e5f04cb40bea311a39118ebeedad210c1`. Existing retained license evidence
was checked before this research. The exact root `LICENSE.txt` copies were
verified against the existing pinned evidence manifests; both repositories are
AGPL-3.0. No additional repositories were used.

The exact Dyson recursive tree contains no Drive controller, service or file
model implementation. It retains `docs/DRIVE_API.md` and upload/encryption/
hierarchy documents. `DRIVE_API.md` documents direct multipart upload, public
open-file, metadata and owner deletion; these provide the bounded compatibility
routes and nullable projection. `FILE_UPLOAD_API_CHANGES.md` contradicts the same
revision's main document about chunk completion (202 processing versus 200 file).
That asynchronous family is explicitly unsupported rather than invented.

Authored Solian `lib/core/drive_wiring.dart` delegates Drive behavior to external
`solar_network_foundation` / `solar_network_sdk` packages. Those packages and
their generated models were not fetched or reused. No excluded SoSYS, Socommon,
Stargate, Spec, NeTo or mirrored/generated definitions were retrieved.

The new raw-stream upload protocol, private default, SHA-256 integrity,
idempotency, quota schema, explicit permissions, guarded SQL transactions,
filesystem adapter and cleanup worker are owned semantics. License retention
does not establish equivalence for unsupported providers or full UI behavior.

The subsequent resumable implementation is described in docs/media-resumable-continuation.md. It adopts the newer authored asynchronous202 completion contract; earlier notes describing all async uploads as unsupported are superseded for those eight routes. Hierarchy, providers and E2EE remain explicit gaps.
