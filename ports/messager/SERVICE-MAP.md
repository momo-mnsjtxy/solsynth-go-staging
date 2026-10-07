# Messaging, realtime, notifications and jobs: bounded source map

Inspected 2026-10-07 using public source only. Source snapshots are observations,
not proof of what is deployed. No application instance was contacted.

## C# messaging boundary

DysonNetwork at commit `4ee2d0cebb9da8284a92483fee6dce795bf6947c` includes
DysonNetwork.Messager (C#, AGPL-3.0 repository license). Chat, membership,
reactions, pins, thread sync, placeholders and voice cleanup remain there.

The [WebSocket infrastructure document](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/CHAT_WEBSOCKET_INFRASTRUCTURE.md)
places the actual WebSocket gateway in external Ring, with Messager pushing
through RemoteWebSocketService/gRPC. Client envelopes contain type, endpoint
and data; service pushes carry type and JSON payload bytes. Device exclusion,
user/device fanout, delivered acknowledgments, typing and call invitations
must remain compatible. This document is architectural evidence, not validation
of all current Ring behavior. The inspected GitHub tree contains no Ring
implementation. Shared is a git submodule pointing to SoSYS/NeTo.

The [room sequence contract](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/CHAT_ROOM_SEQUENCE_SYNC.md)
adds per-room int64 monotonic sequences for persisted events and recovery by
exact IDs or inclusive ranges. Actual source filters invalid selections before
choosing recovery mode; this is more precise than reading the prose alone.

[ScheduledJobsConfiguration.cs](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Messager/Startup/ScheduledJobsConfiguration.cs)
registers Quartz database recycling at midnight, configurable voice cleanup,
and placeholder expiration every minute, waiting for jobs on shutdown.
No scheduler behavior was ported.

## Existing Go boundary

[ElecPostal](https://github.com/Solsynth/ElecPostal/tree/1fad9d225821386e604552e093c18095985d945f)
is already Go, with mail notifications, Ring/realtime clients and SMTP
background delivery. No LICENSE file was found in that inspected tree and
GitHub license metadata was null. It must not be treated as licensed merely
because it is public. Implementation was read for mapping, but none was reused
or translated. Any future implementation needs independently available evidence
with an applicable license or explicit reuse permission. Unlicensed protocol
repository definitions, schemas and generated bindings are excluded, including
as interface-only evidence. Prior implementation exposure remains recorded;
future work must not be mislabeled clean-room after that reading.

Its public go.mod identifies gRPC/protobuf, NATS, Redis, Gin/GORM and
src.solsynth.dev/sosys/go dependencies; these are integration inventory,
not a reason to rewrite existing Go.

## Frontend boundary

[Solian](https://github.com/Solsynth/Solian/tree/ffd0838e5f04cb40bea311a39118ebeedad210c1)
is a Dart/Flutter client (AGPL-3.0 per repository inventory) with chat local
cache/repository, realtime handlers, synchronization and native push adapters.
Its [pubspec](https://github.com/Solsynth/Solian/blob/ffd0838e5f04cb40bea311a39118ebeedad210c1/pubspec.yaml)
includes web_socket_channel, firebase_messaging/core, livekit_client,
flutter_webrtc, flutter_local_notifications, openmls and drift.
The message sync source imports solar_network_sdk and combines remote results
with local and pending caches. Frontend parity requires preserving wire field
names, sequence precision, cursor meaning, packet names and event ordering,
not moving UI code to Go. No frontend was changed or tested.
