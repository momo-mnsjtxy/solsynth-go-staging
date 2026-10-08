# Notification body domain port

`notification_text.go` is a source-informed AGPL-3.0-only translation of
`BuildNotificationBody`, `ports/messager/source/ChatService.cs` lines 1704–1800,
from Solsynth/DysonNetwork revision
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`. The repository license is retained at
`source/LICENSE.txt`. This is a library computation, not a notification service
or a clean-room implementation. No protocol definitions, generated bindings,
or unlicensed shared code were read or incorporated.

Verified local SHA-256 values on 2026-10-07 match `source/manifest.json`:

- ChatService.cs: `75a18068893f878c7a5d24529d88415e632f0947894ceea971620ebf1558fb9a`
- LICENSE.txt: `0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`

The implementation preserves encrypted-before-deleted precedence, special
message kinds, sticker-name-before-preview-before-content selection, exact
localization keys and named arguments, int32 counts, attachment singular versus
plural (including zero), 100 UTF-16-unit preview/content truncation, and the
position of edit/delete changes and mention prefixes. Special kinds ignore the
change suffix, matching the source. Localization output and sticker names are
not truncated. Null and empty locale remain distinct via `*string`.

The result retains UTF-16 code units because the source's substring can split a
surrogate pair. `UTF8()` returns an explicit error on an unpaired surrogate;
it does not invent a wire serialization policy. `UTF16()` returns a defensive
copy for an eventual adapter capable of preserving this source behavior.
Invalid UTF-8 domain input and negative attachment counts are rejected.

Unresolved integration boundaries are deliberate and explicit:

- Callers must supply the established user-encrypted and placeholder-uploading
  classifications; excluded shared helper/model definitions are not guessed.
- Sticker metadata must already be identified as string-valued. A supplied
  sticker count is already an int32; upstream `Convert.ToInt32` conversion and
  its exceptions belong to the future verified metadata adapter.
- `NotificationChange` is an internal classification, not a copied packet
  enum or transport contract. Event mapping still needs a verified adapter.
- The required localization function must provide real locale lookup and
  interpolation, preserving its errors. No fake localization or delivery
  adapter is provided; localized branches fail if it is absent. Source ordering
  is retained even when a later deletion event would replace localized text.
- No recipient selection, authorization, account lookup, push transport,
  persistence, source title/meta construction, or endpoint is implemented.

Tests use synthetic domain inputs and a recording localization test callback
that asserts every expected key, locale and argument. They cover precedence,
special kinds, edit/delete handling, Unicode whitespace, singular/plural paths,
null/empty locale, errors, valid supplementary characters, a split surrogate
at the exact truncation boundary, mention/suffix placement and result aliasing.
`go test -race -cover ./ports/messager` passed using the provisioned Go toolchain
on 2026-10-07. No original .NET differential oracle, real localized catalog,
existing-client end-to-end test or production request was run. These are tested
local policy cases, not full backend or notification compatibility.
