// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"net/url"

	"solsynth.local/go-migration/ports/passport/accounts"
)

// Merchant repository selectors and authored HTTP paths use exact handles.
// Publisher IDs remain separate database identities and negative-route inputs.
func fixturePublisherHandle(p accounts.Publisher) string { return p.Name }

func fixtureMerchantRoute(p accounts.Publisher, resource string) string {
	return "/api/merchants/" + url.PathEscape(fixturePublisherHandle(p)) + "/" + resource
}
