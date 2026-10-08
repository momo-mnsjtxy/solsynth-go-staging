// SPDX-License-Identifier: AGPL-3.0-only
// Source-derived from owned docs/ACCOUNT_BOARD.md and Develop CustomAppService.
package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidOperation = errors.New("board: invalid operation")

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }
func (e *ValidationError) Unwrap() error { return ErrInvalidOperation }
func invalid(message string) error       { return &ValidationError{Message: message} }

// DocumentedEnvelope is an explicitly selected, strict documentation profile.
// This independently authored validation policy accepts the documented envelope
// while preserving JSON values. Extra envelope keys and whitespace labels are
// rejected explicitly; it does not claim undocumented normalization parity.
type DocumentedEnvelope struct{}

func (DocumentedEnvelope) Normalize(ctx context.Context, input Payload) (Payload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := Payload{}
	for name, raw := range input {
		m, ok := raw.(map[string]any)
		if p, yes := raw.(Payload); yes {
			m, ok = map[string]any(p), true
		}
		if !ok {
			return nil, invalid(fmt.Sprintf("Board payload field '%s' must be an object with 'value', 'label', and optional 'format'.", name))
		}
		if _, ok := m["value"]; !ok {
			return nil, invalid(fmt.Sprintf("Board payload field '%s' requires value.", name))
		}
		label, ok := m["label"].(string)
		if !ok || strings.TrimSpace(label) == "" {
			return nil, invalid(fmt.Sprintf("Board payload field '%s' requires a non-empty label.", name))
		}
		for key := range m {
			if key != "value" && key != "label" && key != "format" {
				return nil, invalid(fmt.Sprintf("Board payload field '%s' has an unsupported envelope property.", name))
			}
		}
		if f, exists := m["format"]; exists {
			if _, ok := f.(string); !ok {
				return nil, invalid(fmt.Sprintf("Board payload field '%s' format must be a string.", name))
			}
		}
		if _, err := json.Marshal(m); err != nil {
			return nil, invalid("Board payload must contain JSON values.")
		}
		out[name] = copyValue(m)
	}
	return out, nil
}

type FieldType struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	Format   string `json:"format"`
	Required bool   `json:"required"`
}
type Manifest struct {
	Key             string      `json:"key"`
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	IsEnabled       bool        `json:"is_enabled"`
	RendererType    string      `json:"renderer_type"`
	PayloadType     string      `json:"payload_type"`
	FieldTypes      []FieldType `json:"field_types"`
	RequiredFields  []string    `json:"required_fields"`
	MaxPayloadBytes *int        `json:"max_payload_bytes"`
	AllowMultiple   bool        `json:"allow_multiple"`
}

// ValidateManifestPayload implements the licensed Develop-owned validation order.
// Serialization size uses Go JSON in this synthetic profile; exact System.Text.Json
// escaping and excluded normalizer transformations remain production gates.
func ValidateManifestPayload(ctx context.Context, widget Manifest, payload Payload, normalizer Normalizer) (Payload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !widget.IsEnabled {
		return nil, invalid("Board widget is disabled for this app.")
	}
	if !strings.EqualFold(widget.PayloadType, "object") {
		return nil, invalid("Board widget payload_type must be 'object'.")
	}
	if len(payload) == 0 {
		return Payload{}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, invalid("Board payload must contain JSON values.")
	}
	if widget.MaxPayloadBytes != nil && len(raw) > *widget.MaxPayloadBytes {
		return nil, invalid(fmt.Sprintf("Board widget payload exceeds %d bytes.", *widget.MaxPayloadBytes))
	}
	for _, key := range widget.RequiredFields {
		if _, ok := payload[key]; !ok {
			return nil, invalid(fmt.Sprintf("Board widget payload is missing required field '%s'.", key))
		}
	}
	if normalizer == nil {
		return nil, dependency("normalizer")
	}
	out, err := normalizer.Normalize(ctx, payload)
	if err != nil {
		return nil, err
	}
	for _, field := range widget.FieldTypes {
		raw, exists := out[field.Name]
		if !exists {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, invalid("Board widget payload field must be an envelope.")
		}
		value := m["value"]
		if value == nil {
			continue
		} // Source matcher accepts null for every declared type.
		matches := true
		switch strings.ToLower(field.Type) {
		case "string":
			_, matches = value.(string)
		case "number":
			switch value.(type) {
			case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			default:
				matches = false
			}
		case "boolean":
			_, matches = value.(bool)
		case "object":
			switch value.(type) {
			case map[string]any, Payload:
			default:
				matches = false
			}
		case "array":
			_, matches = value.([]any)
		case "null":
			matches = false
		}
		if !matches {
			return nil, invalid(fmt.Sprintf("Board widget payload field '%s' must be of type '%s'.", field.Name, field.Type))
		}
	}
	return out, nil
}
