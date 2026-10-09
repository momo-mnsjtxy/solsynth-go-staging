// SPDX-License-Identifier: AGPL-3.0-only
package application

import _ "embed"

// Schema is an explicit migration input for the owned taxonomy projection. It
// requires the account module's canonical owned_publishers table first.
//
//go:embed schema.sql
var Schema string
