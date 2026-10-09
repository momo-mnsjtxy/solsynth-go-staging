# Room picture and background customization

This bounded continuation builds on `c0b5fd1ffe2c9b29b3d0ef66f141ce48ea39007e`.
Canonical room create and update now accept `picture_id` and `background_id` and
project current `picture` / `background` file metadata. The existing room,
authority, realm permission, file ownership, byte storage and quota repositories
remain authoritative. No external provider is called.

## Authored evidence and removal boundary

Server sources are pinned to Solsynth/DysonNetwork
`4ee2d0cebb9da8284a92483fee6dce795bf6947c`, AGPL-3.0-only:

- The retained complete `inventory/operation-sources/DysonNetwork.Messager/Chat/ChatRoomController.cs`
  declares both IDs at 650–664, resolves them on create at 753–779, resolves them
  on update at 848–876, and emits old/new slot IDs at 963–975. Both branches run
  only when the ID is non-null. Its original file resolution delegates to
  excluded RPC definitions; those definitions were not inspected or inferred.
- Complete authored `ChatRoomService.cs` (719 lines, Git blob
  `9cf43ace02eac8a03749b157a16e890c102cafc4`) was read. It establishes active
  membership lookup and room hydration. It does not contain image mutation,
  ownership, deletion or removal semantics. Exact source bytes are retained in
  `ports/messager/room-media-evidence`, verified against the prior source manifest.
- Retained `ports/messager/source/ChatService.cs` defines
  `SendChatInfoUpdatedSystemMessageAsync` at 1162–1188: `system.chat.updated`,
  `event=chat_info_updated`, and `changes` containing old/new values. The bounded
  implementation uses this shape for changed image slots and canonical account
  nicknames. Per-room nickname and realm nickname formatting remain outside it.

The complete authored Solian `lib/chat/widgets/chat_room_form.dart` was fetched,
read, retained and checked against Git blob
`4cdfa0e453f169441f76737e0bc69e6bbda9277a` at revision
`ffd0838e5f04cb40bea311a39118ebeedad210c1`, with its AGPL license. It selects cloud
images, populates `room.picture/background`, and sends nullable IDs on
POST/PATCH `/messager/chat`. It contains no image removal control.
`ports/messager/room-media-evidence/manifest.json` pins bytes, hashes and URLs.

Consequently, omitted IDs and explicit null retain the existing references.
Empty IDs, URLs, wrong JSON types, duplicate/case-ambiguous keys and unsupported
IDs fail explicitly. Canonical lowercase UUID IDs are an owned identity choice,
not the upstream 32-character ID contract. The authored form also sends slug and
read-receipt fields that remain explicitly unsupported by the existing host;
this delivery does not claim complete form or excluded model parity.

Reference removal is an explicit owned extension, not attributed to upstream:
DELETE `/api/chat/{id}/media/picture` or `/api/chat/{id}/media/background`, with
an empty body or `{}`, returns 204. The host also exposes these through its
existing `/messager/chat` alias. Repeating a removal is harmless and emits no
duplicate image event. It does not delete the source file or release its quota.
Canonical file deletion hides the current image immediately.

## Canonical mutation and visibility

New references must be ready passive images (PNG, JPEG, GIF, WebP, AVIF or BMP)
owned by the actor. Public visibility or access through another room, post,
profile or realm never substitutes for ownership. The MIME restriction follows
the existing owned profile-image adapter; it is not an image decoding,
transcoding, moderation or malware scan claim.

The normal `chat.create` or `chat.update` verified proof remains required.
Image changes additionally require active membership and current room management
(owner for an ordinary group, participant for a direct room, CanModerateChat for
a linked realm). Attaching requires CanUploadMedia in linked realms. Removal
requires management and membership but not permission to upload a new file.
Existing non-image updates keep their prior rules. No account perk entitlement
is inferred from publisher tiers, and no nickname route is enabled.

The transaction lock order remains sorted canonical accounts, sorted realms,
room, sorted distinct files. File ownership/state is checked after file waits.
Room membership/management, linked social policy and upload permission are
checked before and after those waits. Canonical authority is checked again at
commit, including credential expiry. Reference changes, ordinary fields, room
sequence allocation and update-event insertion roll back together. No-op image
retries produce no duplicate event. Create returns its images without inventing
a creation system message. Other existing room-field updates retain their prior
event behavior; the new event covers image slots only.

`owned_chat_room_media` stores only room/slot/file/author references. A reference
shares that file with the room's current audience without copying bytes,
changing `is_public`, duplicating a quota charge or granting ownership. A stored
authorization belongs to the room until removed/replaced; an author's later role
change does not retroactively erase the room's artwork. The source owner's
current disable/punishment/file lifecycle still suppresses every such grant.

Current room reads and generic file metadata/byte endpoints compose the same
active reference, current ready file, matching canonical owner and live room
policy. Private rooms require active, non-banned room membership and applicable
realm/direct social admission. Public ordinary rooms expose their images to the
public audience. MLS rooms always require membership even if the legacy public
flag remains true. Artwork remains ordinary room metadata; this does not claim
MLS ciphertext or device-key protection for images.

Removing a reference, deleting a room/file, leaving or being removed from a
private room, losing required realm membership, direct blocking, or disabling
the source owner withdraws the corresponding read grant. Existing independent
public/file-owner/profile/post/message grants retain their own semantics. Thus,
a canonically public file remains public when selected for a private room. The
new adapter never promotes a private file merely because its ID is known.
In-flight reads authorized before a later mutation can finish; this is not
recall of previously delivered bytes.

## Installation and verification

Explicitly install `internal/roommediapg/schema.sql` after the canonical chat and
media schemas. Constructors perform no migration or account/quota provisioning.
The host wires one room adapter into chat hydration/mutation and the canonical
media visibility chain; readiness checks the new table.

Eight guarded PostgreSQL test declarations cover canonical and HTTP composition,
create/update/null/removal behavior, quota conservation, foreign public-file
rejection, passive MIME validation, current audience and deleted resources,
atomic room/reference/event rollback, idempotent event behavior, concurrent slot
updates, media/room/account/realm lock waits, membership/global/realm revocation,
proof expiry, direct blocking, MLS privacy and source-owner disablement. The
restart test saves the exact room/reference/event snapshot and file paths before
the restart, then reads that saved state and original bytes after it without
reseeding. The required registry contains 588 before / 77 after cases on this
base. Offline compilation skips actual database tests without `PG_TEST_DSN`;
real PostgreSQL, locks and restart execution remain for authorized private CI.

Source integrity, mutation rejection, integration wiring and PostgreSQL-16 syntax
checks are part of `scripts/verify.sh`, together with build, vet, full Go tests
and race tests. Syntax checks do not establish catalog/type/locking correctness.
No live service, unlicensed repository, excluded Shared/NeTo/Spec/generated
source, canceled SoSYS/Socommon source, local database socket or publication was
used in this delivery. Full original DTOs, slug/read-receipt customization,
member nicknames, threads/forwards and real push/WebSocket publication remain
separate flows.
