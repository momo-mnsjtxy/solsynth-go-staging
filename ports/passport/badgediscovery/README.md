# Badge discovery HTTP family

Source-derived from the owned AGPL DysonNetwork controller at
4ee2d0cebb9da8284a92483fee6dce795bf6947c. This public family has no database,
authentication, permission, optional-user, RPC or shared row/audit dependency.
No excluded protocol definitions or generated bindings are used.

## API

NewHandler(Config{IconsPath, TraceID}) returns an http.Handler.
RouteMatches(path) identifies this family for host routing.
Catalog(hasIcons) returns a detached Manifest containing all 23 original entries.

- GET /.well-known/badges: version=1, ordered badges, explicit null series and
  icon_url when appropriate; no filtering of hidden entries
- GET /.well-known/badges/icons/{iconName}: configured directory file named
  {iconName}.svg, raw bytes, image/svg+xml and exact Content-Length
- Cache-Control: public,max-age=3600 for manifest; public,max-age=86400 for icon
  action responses, including documented icon 404 errors
- GET only; other methods including HEAD return 405 with Allow: GET and no body
- Route literals are case-insensitive and accept a single trailing slash;
  filenames preserve case and follow the host filesystem's case sensitivity
- Range and conditional request headers do not enable partial responses or
  validators. No ETag, Last-Modified, Accept-Ranges or Content-Range is invented

The manifest advertises all configured URLs whenever IconsPath is a directory,
including an empty directory. It does not individually test icon existence.
Missing/blank configuration does not implicitly load bundled assets.
The upstream example configuration is ./Resources/BadgeIcons. To serve the
included distribution explicitly, configure this package's assets directory.

Known errors retain PASSPORT_BADGE_ICON_NOT_FOUND (404) and
PASSPORT_BADGE_ICON_INVALID_PATH (400). The documented ApiError code/message/
status/traceId shape is used, omitting absent optional fields. TraceID can use
the host request trace; the default is a process-local correlation sequence.
Unhandled read errors use the documented generic SERVER_ERROR (500), not
upstream stack traces or filesystem paths.

## Explicit security and resource differences

Path confinement deliberately uses os.Root and single-segment names. It does not
reproduce the original lexical string-prefix check. Multi-segment/absolute/NUL
names and symlinks escaping the configured root are refused. This hardening is
intentional, not a claim of legacy adversarial-path compatibility.

The bundled whatshot.svg is ORIGINAL replacement artwork authorized for the two
hidden repeated-downvote badges. Its filename/API role is preserved; its geometry
and pixels intentionally differ from the unproven upstream asset. The unknown
upstream whatshot image is not copied. See assets/ORIGINAL-ART.md.

The other twelve assets match official Google Material Symbols geometry. Their
Apache-2.0 license, pinned source URLs, comparison method, presentation changes
and attribution are in assets/APACHE-2.0.txt and assets/PROVENANCE.json. Per-file
comments record modifications. Added comments change served bytes, while
equivalent path geometry and original configured fill/size preserve appearance.
The package/controller adaptation is AGPL; do not remove either license notice.
No Fortune quotation corpus is included.

## Evidence and verification

- [Owned controller/manifest](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/Progression/BadgesDiscoveryController.cs)
- [Owned configuration](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/DysonNetwork.Passport/appsettings.json)
- [Owned API error document](https://github.com/Solsynth/DysonNetwork/blob/4ee2d0cebb9da8284a92483fee6dce795bf6947c/docs/SHARED_API_ERRORS.md)
- [Google license](https://github.com/google/material-design-icons/blob/737e3324305806514d7909874fa1818ae1808232/LICENSE)

Package tests cover manifest/order/nulls, configuration, error/cache headers,
methods/routes, full SVG responses, conditional/range requests, root confinement
and complete bundled resource availability. Independent .NET MVC fixtures and
cross-runtime checks live under tests/contract/badges-oracle; their results are
tracked separately by the validator. No production deployment is performed.
