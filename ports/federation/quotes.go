// SPDX-License-Identifier: AGPL-3.0-only
package federation

import "context"

type QuoteAuthorization struct {
	Context           any    `json:"@context"`
	Type              string `json:"type"`
	ID                string `json:"id"`
	AttributedTo      string `json:"attributedTo"`
	InteractingObject string `json:"interactingObject"`
	InteractionTarget string `json:"interactionTarget"`
}

// Creation is admitted only through an existing verified inbox QuoteRequest.
type QuoteRepository interface {
	QuoteAuthorization(context.Context, string) (QuoteAuthorization, error)
	RevokeQuoteAuthorization(context.Context, string, string) error
}
