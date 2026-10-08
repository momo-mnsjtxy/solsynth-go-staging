// SPDX-License-Identifier: AGPL-3.0-only
// Source-informed from licensed DysonNetwork controllers and BOT_CHAT_API.md,
// revision 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-08.
package develop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
)

var (
	ErrBotCommandsPolicyUnverified = errors.New("bot commands policy unverified")
	ErrBotNotFound                 = errors.New("bot not found")
	ErrBotCommandsUnavailable      = errors.New("bot commands unavailable")
	ErrBotCommandsInvalidID        = errors.New("invalid bot ID")
	ErrBotCommandsUnsupportedJSON  = errors.New("unsupported synthetic bot command JSON")
)

// BotCommand and BotCommandParameter follow the explicit data-model tables in
// licensed BOT_CHAT_API.md. No excluded shared model implementation is copied.
type BotCommand struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Usage       *string               `json:"usage"`
	Parameters  []BotCommandParameter `json:"parameters"`
}
type BotCommandParameter struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Required    bool    `json:"required"`
	Type        *string `json:"type"`
}

type BotCommandsReader interface {
	ReadBotCommands(context.Context, string) ([]BotCommand, error)
}
type BotCommandsService struct {
	reader    BotCommandsReader
	candidate bool
}

func NewBotCommandsService(reader BotCommandsReader) *BotCommandsService {
	return &BotCommandsService{reader: reader}
}
func NewSyntheticCompatibilityBotCommandsService(reader BotCommandsReader) *BotCommandsService {
	return &BotCommandsService{reader: reader, candidate: true}
}

// ValidateBotCommandID accepts canonical UUID text only. ASP.NET's Guid route
// constraint may accept other forms; those are an explicit candidate boundary.
func ValidateBotCommandID(id string) error {
	if len(id) != 36 {
		return ErrBotCommandsInvalidID
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return ErrBotCommandsInvalidID
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return ErrBotCommandsInvalidID
		}
	}
	return nil
}

func (s *BotCommandsService) Read(ctx context.Context, id string) ([]BotCommand, error) {
	if s == nil || !s.candidate {
		return nil, ErrBotCommandsPolicyUnverified
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateBotCommandID(id); err != nil {
		return nil, err
	}
	if s.reader == nil {
		return nil, ErrBotCommandsUnavailable
	}
	commands, err := s.reader.ReadBotCommands(ctx, strings.ToLower(id))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if commands == nil {
		commands = []BotCommand{}
	}
	return commands, nil
}

const MaxSyntheticBotCommandsJSONBytes = 1 << 20

// DecodeSyntheticBotCommands accepts a documented-wire-shaped fixture format,
// NOT an inferred production Npgsql mapping. DynamicJson storage and MVC JSON
// use separate options upstream. Unknown/mis-cased fields fail closed instead
// of being silently dropped or echoed. Required fields must be explicit; absent
// optional fields become null under this deliberately selected fixture policy.
func DecodeSyntheticBotCommands(data []byte) ([]BotCommand, error) {
	if len(data) > MaxSyntheticBotCommandsJSONBytes {
		return nil, ErrBotCommandsUnsupportedJSON
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, ErrBotCommandsUnsupportedJSON
	}
	commands := make([]BotCommand, 0, len(raw))
	for _, item := range raw {
		fields, err := botCommandObject(item, "name", "description", "usage", "parameters")
		if err != nil {
			return nil, err
		}
		var command BotCommand
		if err = botCommandRequired(fields, "name", &command.Name); err != nil {
			return nil, err
		}
		if err = botCommandRequired(fields, "description", &command.Description); err != nil {
			return nil, err
		}
		if value, ok := fields["usage"]; ok {
			if err = json.Unmarshal(value, &command.Usage); err != nil {
				return nil, ErrBotCommandsUnsupportedJSON
			}
		}
		if value, ok := fields["parameters"]; ok {
			var parameters []json.RawMessage
			if err = json.Unmarshal(value, &parameters); err != nil {
				return nil, ErrBotCommandsUnsupportedJSON
			}
			if parameters != nil {
				command.Parameters = make([]BotCommandParameter, 0, len(parameters))
			}
			for _, parameter := range parameters {
				pfields, err := botCommandObject(parameter, "name", "description", "required", "type")
				if err != nil {
					return nil, err
				}
				var parsed BotCommandParameter
				if err = botCommandRequired(pfields, "name", &parsed.Name); err != nil {
					return nil, err
				}
				if err = botCommandRequired(pfields, "required", &parsed.Required); err != nil {
					return nil, err
				}
				if value, ok := pfields["description"]; ok {
					if err = json.Unmarshal(value, &parsed.Description); err != nil {
						return nil, ErrBotCommandsUnsupportedJSON
					}
				}
				if value, ok := pfields["type"]; ok {
					if err = json.Unmarshal(value, &parsed.Type); err != nil {
						return nil, ErrBotCommandsUnsupportedJSON
					}
				}
				command.Parameters = append(command.Parameters, parsed)
			}
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func botCommandObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, ErrBotCommandsUnsupportedJSON
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, ErrBotCommandsUnsupportedJSON
	}
	for key := range fields {
		known := false
		for _, allowed := range keys {
			if key == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, ErrBotCommandsUnsupportedJSON
		}
	}
	return fields, nil
}
func botCommandRequired(fields map[string]json.RawMessage, key string, target any) error {
	value, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return ErrBotCommandsUnsupportedJSON
	}
	if err := json.Unmarshal(value, target); err != nil {
		return ErrBotCommandsUnsupportedJSON
	}
	return nil
}
