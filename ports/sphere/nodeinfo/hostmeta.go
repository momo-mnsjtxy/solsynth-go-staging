// SPDX-License-Identifier: AGPL-3.0-only
// Derived from licensed DysonNetwork WebFingerController.GetHostMeta at
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.
package nodeinfo

import (
	"bytes"
	"encoding/xml"
)

// HostMeta renders only configuration; it performs no account or remote lookup.
// Missing configuration uses localhost; explicit empty configuration stays empty.
// Escaping the attribute is a deliberate correctness/safety improvement over
// direct source interpolation when a domain contains XML metacharacters.
func HostMeta(domain *string) []byte {
	value := "localhost"
	if domain != nil {
		value = *domain
	}
	var attribute bytes.Buffer
	_ = xml.EscapeText(&attribute, []byte("https://"+value+"/.well-known/webfinger?resource={uri}"))
	return []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<XRD xmlns=\"http://docs.oasis-open.org/ns/xri/xrd-1.0\">\n  <Link rel=\"lrdd\" template=\"" + attribute.String() + "\"/>\n</XRD>")
}
