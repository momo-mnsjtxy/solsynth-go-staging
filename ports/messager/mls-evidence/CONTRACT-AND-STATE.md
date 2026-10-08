# Authored MLS delivery contracts and owned implementation responsibilities

## Result

An owned, authenticated opaque MLS delivery/control plane is feasible from the
licensed client-facing evidence. The routes and payload fields are substantially
available; absent legacy internals are not a permanent blocker. Encrypted rooms
are still disabled in the delivered rich-chat patches. This report precedes any
enablement and makes no claim of cryptographic MLS validation or interoperability.

Five complete authored Solian files at
`ffd0838e5f04cb40bea311a39118ebeedad210c1` are retained alongside `manifest.json`.
Every Git blob SHA was checked against the connector response and existing pinned
tree selection. Three authored DysonNetwork documents at
`4ee2d0cebb9da8284a92483fee6dce795bf6947c` have the same verification in
`server-docs-manifest.json`. The respective AGPL licenses are retained.
Existing licensed Messager controllers and Solian message wrappers remain in
`ports/messager/source` and `ports/messager/contract-evidence` of the code snapshot.
No excluded schemas, generated bindings, NeTo/Spec, SoSYS/Socommon or Suki were
read or retrieved. No live service/account endpoint was probed.

## Exact authored caller surface

The following paths are relative to the configured Stargate client. Its network
base/host should be composed through the existing owned host aliases rather than
hardcoding a deployment URL. Authentication is the existing verified bearer
proof. MLS calls additionally supply `X-Device-Id`; that header is a device
selector, never a replacement for verified account authority.

| Operation | Method/path | Authored body or query | Consumed response |
|---|---|---|---|
| Publish key package | PUT /e2ee/mls/devices/me/kps | key_package:string, device_id:string | Success status; bytes are opaque base64 |
| Device key packages | GET /e2ee/mls/keys/{accountId}/devices | No consume flag in this caller | Array; device_id and key_package |
| Own package status | GET /e2ee/mls/kp/status | Header device selector | needs_more_kps:boolean, devices_needing_kps:[{device_id,available_count,...}] |
| Account readiness | POST /e2ee/mls/users/ready/batch | account_ids:[string] | users:[object] |
| Revoke own device | POST /e2ee/mls/devices/{deviceId}/revoke | No body | 200 or 204 |
| Reserve group bootstrap | POST /e2ee/mls/groups/{groupId}/bootstrap | epoch:0, state_version:1, meta:{bootstrap_device_id} | Object; meta.bootstrap_device_id must equal this device before local group creation |
| Commit pending state | POST /e2ee/mls/groups/{groupId}/commit | epoch:current local epoch, reason:"client_commit" | Object; epoch is consumed |
| Publish public GroupInfo | PUT /e2ee/mls/groups/{groupId}/groupinfo | group_info:base64, ratchet_tree:base64, epoch:integer | 200 or 204 |
| Read public GroupInfo | GET /e2ee/mls/groups/{groupId}/groupinfo | Header device selector | group_info, ratchet_tree |
| Welcome fanout | POST /e2ee/mls/groups/{groupId}/welcome/fanout | recipient_account_id, payloads:[{recipient_device_id,ciphertext,header,client_message_id,meta:{invited_by}}] | Success |
| Commit fanout | POST /e2ee/mls/groups/{groupId}/commit/fanout | epoch, ciphertext, header, client_message_id, meta:{reason,client_message_id} | Success |
| Application fanout helper | POST /e2ee/mls/messages/fanout | room_id:groupId, payload:encryptedPayload | Map |
| Device queue | GET /e2ee/mls/envelopes/pending | No query; X-Device-Id | Array; id, type:int, ciphertext, group_id, header |
| Acknowledge queue record | POST /e2ee/mls/envelopes/{id}/ack | Query deviceId; X-Device-Id | 200 or 204 |
| Request recovery | POST /e2ee/mls/groups/{groupId}/reshare-required | Empty object | 200 or 204 |
| Own recovery list | GET /e2ee/mls/devices/me/reshare-required | Header device selector | Array; mls_group_id is used |
| Complete recovery | POST /e2ee/mls/devices/me/reshare-required/{groupId}/complete | No body | Success |
| Reset group | POST /e2ee/mls/groups/{groupId}/reset | new_epoch:0, state_version:local version+1, reason | Success; 404 permits later bootstrap |

Primary file/line anchors: identity manager 54–57, 239–360; group manager
95–110, 283–306, 450–474, 625–725, 769–878, 907–959, 1102–1134;
message handler 565–633. The authored group-manager enum explicitly maps
welcome=5, commit=4, proposal=7, application=6. These values do not come from
an excluded shared enum. Header-local type values 1/2 embedded in opaque base64
are distinct from outer envelope type numbers and must not be conflated.

Owned canonical chat endpoints are also evidenced:

- POST /api/chat/{room}/mls/enable accepts optional mls_group_id (at most 256
  characters), e2ee_policy object. The controller authorizes a direct-room
  member, group owner, or realm moderator; encryption cannot be disabled.
  Default group ID is room:{room UUID}. An owned implementation should bind a
  supplied group ID uniquely to this canonical room and refuse reuse by another
  room. This is an independent isolation rule, not inferred legacy storage.
- POST /api/chat/{room}/devices/me/joined takes mls_device_id and epoch, after
  current room membership and MLS-room checks. The verified account is passed
  to the delivery service by the controller, never supplied by the client.
- Standard message POST/PATCH/DELETE routes transport encryption_meta with
  ciphertext, header, signature (nullable), scheme="chat.mls.v2", and epoch.
  The authored message builder returns type and attachment/reference metadata;
  its wrapper removes plaintext_envelope before HTTP. Client code explicitly
  parses room encryption mode 3 as MLS and 0 as plaintext. The owned runtime
  can introduce an explicit MLS policy value rather than guess shared schemas.
- Plaintext content is forbidden for encrypted writes. The current authored
  controller checks scheme and exact current epoch. Reactions/read/typing/system
  controls remain plaintext. Server delivery must not decrypt or generate keys.

Relevant local anchors: ChatRoomController.cs 1009–1078; ChatController.cs
639–656, 1106–1132, 1312–1336, 1804–1844; authored chat_room.dart 146–164;
e2ee_message_service.dart 104–143; mls_message_handler.dart 197–227.

## Owned state and invariants

This is a proposed implementation design, not a report of code already present.

1. Device records bind an opaque bounded device identifier to the authenticated
   canonical account. A client device ID is a timestamp-derived base64url string,
   not a UUID. Repeated upload cannot transfer device ownership. Header/body/
   ack-query selectors must agree, and selectors from another account are denied.
   Revocation is durable and immediately gates reads, fanout and acknowledgments.
2. Key-package rows hold opaque bytes, a digest, upload time and consumption state.
   Claiming a package uses a transaction/row lock and commits consumption with
   the returned claim, so two concurrent requests cannot receive one package.
   Retry semantics need careful definition because this caller supplies no
   explicit claim idempotency key. Registration is not proof that the bytes are
   a valid MLS KeyPackage. Rate/size/retention bounds are owned service policy.
3. Group rows have one canonical room, bootstrap account/device, delivery epoch,
   reset generation/state_version, public GroupInfo and ratchet-tree snapshots.
   Bootstrap is reserved atomically and retries cannot reset an existing epoch.
   GroupInfo writes compare the submitted epoch to the current group row.
4. Account/device group memberships inherit fresh room membership, realm/direct
   restrictions, disabled-account state and device revocation. Joining/recovery
   states must be explicit. A claimed epoch alone never grants room access.
5. Fanout locks the group, validates sender authority and target account/device
   membership, snapshots recipients, allocates per-device sequence numbers and
   inserts all envelopes atomically. Client message IDs plus request digests
   prevent retries from advancing epochs or duplicating delivery. Welcome delivery
   must reach only eligible target devices, never a caller-selected outsider.
6. Pending reads and acknowledgments revalidate the caller's account/device and
   current canonical group access. Acknowledgment affects only that device's
   envelope. Order is stable across restart; failed/unacknowledged records remain
   available. Deletion/removal/revocation withdraws unauthorized pending delivery.
7. Chat ciphertext send/edit/delete uses the same group-generation/epoch snapshot
   as canonical message mutation and recovery-event append. No local plaintext
   envelope, private signer key, or group secret is stored. Opaque attachment
   references retain the canonical file ownership/visibility rules.

The implementation can claim authenticated account/device delivery and durable,
ordered, isolated state transitions after testing these invariants. It cannot
claim MLS signature validation, encrypted-message integrity, peer credential
validation, confidentiality or successful decryption from JSON transport tests.

## Client/server security boundary and unresolved tradeoffs

RFC 9750 separates delivery from authentication and explains that credential
binding and transport sequencing require application decisions. An ordering
service can serialize competing commits, while members perform MLS processing.
This supports an opaque server implementation, but does not turn account/header
checks into cryptographic verification. See
https://www.rfc-editor.org/rfc/rfc9750.html#section-2.2 and
https://www.rfc-editor.org/rfc/rfc9750.html#section-5.2.1 .

- A bearer-authenticated upload can be attributed to an account. The current
  wire fields do not prove that an arbitrary package's embedded credential is
  that declared device or that a client possesses its signing key. A vetted MLS
  parser/verifier could validate existing KeyPackage bytes without changing
  this request shape. If stronger per-device possession or re-enrollment proof
  is required, a challenge/signature flow is a new client contract and should be
  called out explicitly. Treating X-Device-Id itself as secret proof is invalid.
- Opaque Commit acceptance can enforce a monotonic delivery epoch, not prove
  the ciphertext contains the advertised membership transition. Clients must
  validate/apply commits and rotate keys on removal. The server cannot generate
  replacement group secrets or claim cryptographic revocation simply by removing
  a delivery row. MLS protocol validation belongs in the clients or a separately
  specified public-message validator. RFC 9420 describes member-side protocol
  processing: https://www.rfc-editor.org/rfc/rfc9420.html .
- New-device external join reads GroupInfo before registering joined state in
  the authored caller. The server document says active device membership is
  required for GroupInfo. An explicit authorized joining state, derived from
  current room membership and account-owned device registration, can resolve
  this without granting strangers access; it must be a reviewed behavior choice.
- The package getter omits consume=true even though docs describe a consume
  option. Prefer a documented consuming default for this specific compatibility
  route with serialized one-use claims; alternatively a client correction is
  needed. Do not silently serve the same package repeatedly as if claims were
  one-use. Account/device recovery code also passes a device ID to the account-ID
  getter and reads key_package_bytes, while normal group-add reads key_package.
  That mismatch needs either a bounded compatibility adapter or client fix.
- Documentation is internally inconsistent: old client-ability requirements
  remain in status/migration prose, while newer delivery prose and actual callers
  omit that header. Docs mix camelCase with actual snake_case. Application fanout
  docs show recipient payload arrays, while the authored helper submits room_id
  plus payload. Implement actual caller maps; report other shapes unsupported
  until required, rather than infer undocumented serializer behavior.
- Key limits and envelope storage are not cryptographic guarantees. Canonical
  device/account ownership and current room authority must be checked on every
  operation even when a group ID or ciphertext looks valid.

## Concrete attachment confidentiality concern

The authored mls_message_handler.dart at 29–35 derives the file encryption key
from only a constant label and the room identifier. messages_notifier.dart at
230–231 selects it for MLS rooms; message_sender.dart at 664–669 passes it as
`encryptPassword` to the uploader. The derivation contains no secret input. Thus
this source cannot substantiate end-to-end attachment secrecy against anyone who
knows that identifier. This is a source-supported concern, not a live deployment
exploit test. Do not copy this derivation into Go or advertise E2EE file security.
Opaque already-encrypted file storage remains possible, but a secure client key
scheme and key distribution contract need separate treatment.

## Suggested implementation boundary

First implement the bounded authenticated device/key-package store, canonical
room/group bootstrap, monotonic delivery epoch, current-epoch GroupInfo, isolated
ordered envelope fanout/pending/ack, and device revocation. Keep encryption
capability explicitly disabled until these transaction/restart/isolation tests
pass and an authored-client test establishes the expected handshake sequence.
Then add opaque chat ciphertext transport with exact scheme/epoch checks and
plaintext-field rejection. Any unimplemented cryptographic validation must remain
explicit in capability reporting; no shared/generated schema is needed for this
owned design. Neither step should claim to solve the client attachment-key issue.
