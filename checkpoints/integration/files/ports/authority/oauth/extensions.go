// SPDX-License-Identifier: AGPL-3.0-only
package oauth

import "context"

const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}
type DeviceRepository interface {
	DeviceAuthorize(context.Context, string, string, string) (DeviceAuthorization, error)
	DevicePreview(context.Context, string, string) (ClientInfo, error)
	DeviceDecide(context.Context, string, string, bool) error
}
type IntrospectionRepository interface {
	Introspect(context.Context, string, string, string) (map[string]any, error)
}

func DeviceFailure(code string) error {
	return &Failure{Code: code, Description: "Device authorization is pending, denied, expired or already consumed.", Status: 400}
}
