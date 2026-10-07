# Auth and board protocol-source scope audit

Date: 2026-10-07

The user clarified that the protocol-definition repository is unlicensed and
must not be used. Earlier suggestions to obtain its .proto files or descriptor
exports are rescinded. This audit records the correction without deleting
existing licensed source evidence or changing its provenance.

## Inspected scope

- internal/auth
- ports/passport/board
- ports/passport/username, including board specification and architecture notes

## Findings

No .proto, descriptor-set or generated .pb.* files were found in this scope.
The Go files in the username and board packages do not import the separate
SoSYS Go protocol package, google.golang.org/protobuf or google.golang.org/grpc.
There is no auth implementation or RPC adapter in internal/auth.

The board Item JSON projection and kind values are independently transcribed
from the licensed Solian client at ffd0838e5f04cb40bea311a39118ebeedad210c1.
The historical board schema.json is reconstructed from two DysonNetwork-owned
AGPL-licensed EF migration files at 4ee2d0cebb9da8284a92483fee6dce795bf6947c:

- DysonNetwork.Passport/Migrations/20260708150813_AddAccountBoardItems.cs
- DysonNetwork.Passport/Migrations/20260708152943_AddCustomAppWidgetKeyToAccountBoardItems.cs

It is a database evidence fixture, not a schema copied from the unlicensed
protocol repository. Board behavior derives from the licensed parent repository's
own AccountBoardService/controller call sites. Local interfaces are hand-written
adapter boundaries, not generated bindings.

The retained licensed AccountUsernameService.cs includes a reference to a Shared
namespace; it does not contain copied protocol definitions. Documentation names
RPC/message/permission symbols observed at licensed call sites but does not
include unseen protocol field numbers, descriptor contents or generated bindings.
These symbol references do not establish wire compatibility.

## Corrected documents

- internal/auth/CONTRACT.md
- internal/auth/HTTP_RECONSTRUCTION.md
- ports/passport/username/BOARD_INTERFACE_SPEC.md
- ports/passport/username/ARCHITECTURE.md
- ports/passport/username/parity.json
- ports/passport/board/README.md

The request for original protocol/descriptor exports has been removed from the
active input path. A dated note explains that the earlier proposal was rescinded.
Permitted remaining evidence is independently derived licensed-client/call-site
contracts or separately licensed public documentation. The excluded repository
must not be fetched, exported by the user, copied or used as a shortcut.

## Runtime consequence

No code changes were needed for this correction. Missing authentication,
permission and payload authority adapters remain absent or fail closed. No
production-ready auth/board transport is claimed. No repository fetch, remote
write, dependency import, credential use or source-history deletion occurred
during this audit.
