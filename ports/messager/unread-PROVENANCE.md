# Unread aggregation policy

Source-informed AGPL-3.0-only Go translation, modified 2026-10-07. This is executable computation over caller-provided snapshots, not a messaging endpoint or database adapter.

## Eligible source

Solsynth/DysonNetwork revision `4ee2d0cebb9da8284a92483fee6dce795bf6947c`, retained locally as `source/ChatService.cs` (SHA-256 `75a18068893f878c7a5d24529d88415e632f0947894ceea971620ebf1558fb9a`): `ChatUnreadSummary` at lines 24–28, `CountUnreadMessage` and `CountUnreadMessageForUser` at lines 1961–2054, and `PlaceholderContentType` at line 2795. Exact source manifest and upstream AGPL text remain in `source/manifest.json` and `source/LICENSE.txt`. `source/DysonNetwork.Messager__AppDatabase.cs` declares the two sets and invokes `ApplySoftDeleteFilters` at line 144; its implementation is unavailable here.

No upstream retrieval, production request, unlicensed repository, protocol descriptor or generated binding was used. Shared model definitions and notification enum numeric values are not available in this licensed local snapshot. Projection field requirements come only from these licensed callers; symbolic notification constants are a new internal API, not a guessed wire enum.

## Computation preserved

Only joined, non-left memberships for the account participate. Single-room queries select the first eligible supplied member. Messages must be in the room, from another member, neither `placeholder` nor a type beginning with case-sensitive `messages.`, and strictly after LastReadAt when present. Mentions compare account IDs, not member IDs; repeated mentions do not multiply counts. No thread exclusion is present in these source methods.

Single-room None returns zero. Multi-room HasUnread ignores notification preferences; UnreadCount includes All or matching Mentions only. Consequently an unknown preference counts eligible messages in the single-room method but not the multi-room method. These differing source predicates are retained intentionally. Every eligible room gets a summary even without messages. A nil room selection means all eligible rooms; an explicitly empty selection means none. Duplicate selected room IDs do not multiply counts. Duplicate active memberships do multiply multi-room join counts; single-room selection retains snapshot order because upstream FirstOrDefault has no explicit ordering.

Counts use signed 32-bit storage and return an explicit overflow error rather than wrapping. The upstream group Count for HasUnread is bounded too. Errors expose no partial result. Functions do not mutate input rows, slices or read timestamps, and do not retain them after returning. IDs use decoded 16-byte values and time comparisons use time.Time instants; there is no wire codec or request validation implied.

## Remaining gaps

The caller must supply snapshots after the original database global filters. Those filters, soft-deletion visibility, model nullability, numeric enum mapping, database collation, timestamp storage precision and full GUID/JSON codecs remain unverified. This package must not be fed raw production rows as though it supplied those filters. Type is an explicitly supplied non-null Go string; no nullable SQL Type semantics are inferred.

The original multi-room method issues multiple database queries, which may observe different concurrent states; this API computes one consistent supplied snapshot. Database transactions, persistence, actual read-receipt updates, clock selection, cache invalidation, WebSocket events, authorization, room visibility, notifications and HTTP remain unimplemented. This policy must not establish membership or permission. No production route is enabled.

Tests use synthetic inputs covering strict time boundaries and offsets, self/control/placeholder exclusions, notification modes and mention identity, inactive membership, selected room semantics, empty-room summaries, duplicate membership behavior and overflow. They are not a .NET differential oracle or database/global-filter compatibility proof.
