# Message heading formatting policy

This is a source-informed, transport-independent Go port, modified 2026-10-07,
under AGPL-3.0-only. It adds no HTTP route, authentication, persistence or message
delivery. It is not a completed messaging service or a Markdown/HTML sanitizer.

## Eligible source evidence

- Repository: Solsynth/DysonNetwork, pinned revision
  `4ee2d0cebb9da8284a92483fee6dce795bf6947c`.
- Retained source: `source/ChatService.cs`, lines 3098–3167, methods
  `EscapeMarkdownHeadings` and `EscapeMarkdownHeadingMarkers`.
- Source SHA-256 verified against `source/manifest.json`:
  `75a18068893f878c7a5d24529d88415e632f0947894ceea971620ebf1558fb9a`.
- License: retained `source/LICENSE.txt`, GNU AGPL version 3, SHA-256
  `0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`.
- No excluded NeTo/Spec definitions, descriptors or generated bindings were used.
  No new upstream retrieval, production request or denied/cancelled retry occurred.

## Scope and exact behavior

`EscapeMarkdownHeadingMarkers(*string)` preserves nullable strings. It splits
only on LF and preserves CR, trailing LF and other Unicode characters. Outside
fences it inserts one backslash before 1–6 leading hashes after at most three
ASCII spaces, only when the hashes end the line or precede C# whitespace.
Tabs and Unicode whitespace do not count as heading indentation.

Fence detection trims all source-compatible whitespace, accepts any indentation,
and opens on three or more identical backticks or tildes with arbitrary trailing
text. Closing requires the original marker string followed only by whitespace:
a *longer* fence does not close, even though Markdown implementations may allow
it. Different markers, shorter runs and closing info strings also do not close.
Short leading backtick/tilde runs skip heading processing without changing state.

`FormatMessageContent(content, hasEncryptionMeta)` preserves all content when
encryption metadata is present, including empty metadata. This boolean must come
from actual metadata presence in a future adapter. No message DTO shape is
invented here. Helpers never mutate inputs; unchanged output pointers may alias.

The Go policy explicitly enumerates the BMP whitespace set corresponding to
C# `char.IsWhiteSpace` and default `Trim` behavior. Valid UTF-8 is the portable
input contract. Invalid Go byte strings are retained but cannot claim parity
with unpaired C# UTF-16 surrogates. Astral non-whitespace content is preserved.

## Verification and remaining gaps

`formatting_test.go` covers null/empty, encryption bypass, whitespace and
non-whitespace Unicode, one/six/seven hashes, indentation boundaries, LF/CRLF,
lone CR and Unicode separators, mixed/short/long fences, info strings, unclosed
fences, unchanged input, idempotence and 10,000 synthetic lines.

Executed on 2026-10-07 using the real Go 1.26.8 toolchain:
`go test -race -cover ./ports/messager` and `go vet ./ports/messager` both passed.
Package-wide coverage is not a formatting-only metric because other independent
Messager ports share this package. The parent verification record records the
final combined suite after all concurrent module work is complete.
No original .NET differential oracle has run. Tests establish these synthetic
cases, not end-to-end compatibility. Full DTO/null serialization, authorization,
membership/visibility, encrypted message parsing, hydration, database adapters,
events and frontend parity remain unresolved as documented in
`ROOM-SYNC-CONTRACT.md` and `../../docs/messager-parity.json`.
