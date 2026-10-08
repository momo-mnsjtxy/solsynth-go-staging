// SPDX-License-Identifier: AGPL-3.0-only
package exchangecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// LoadJSON loads a UTF-8 appsettings-style JSON object. It preserves source
// order, flattens ':' paths, uses .NET boolean spelling, and accepts comments
// and trailing commas like the .NET JSON configuration provider. Null/empty
// objects become nil; empty arrays become empty strings. It does not watch files
// or import environment/command-line providers implicitly.
func LoadJSON(reader io.Reader) (Provider, error) {
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(raw) {
		return nil, errors.New("configuration must be UTF-8")
	}
	clean, err := cleanJSON(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(clean))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, errors.New("configuration root must be an object")
	}
	p := &providerParser{decoder: decoder, index: map[string]int{}}
	if err = p.object("", 0); err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, err
	}
	return p.entries, nil
}

type providerParser struct {
	decoder *json.Decoder
	entries Provider
	index   map[string]int
}

func (p *providerParser) set(path string, value *string, overwrite bool) error {
	key := ordinalKey(path)
	if i, exists := p.index[key]; exists {
		if !overwrite {
			return fmt.Errorf("duplicate configuration key %q", path)
		}
		p.entries[i].Value = value
		return nil
	}
	p.index[key] = len(p.entries)
	p.entries = append(p.entries, Entry{Key: path, Value: value})
	return nil
}
func configPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + ":" + key
}
func (p *providerParser) object(path string, depth int) error {
	if depth >= 64 {
		return errors.New("configuration exceeds JSON depth 64")
	}
	empty := true
	for p.decoder.More() {
		empty = false
		token, err := p.decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("invalid object key")
		}
		child := key
		if depth > 0 {
			child = path + ":" + key
		}
		if err = p.value(child, depth+1); err != nil {
			return err
		}
	}
	if _, err := p.decoder.Token(); err != nil {
		return err
	}
	if empty && depth > 0 {
		return p.set(path, nil, true)
	}
	return nil
}
func (p *providerParser) value(path string, depth int) error {
	token, err := p.decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			return p.object(path, depth)
		case '[':
			if depth >= 64 {
				return errors.New("configuration exceeds JSON depth 64")
			}
			i := 0
			for p.decoder.More() {
				if err = p.value(path+":"+strconv.Itoa(i), depth+1); err != nil {
					return err
				}
				i++
			}
			if _, err = p.decoder.Token(); err != nil {
				return err
			}
			if i == 0 {
				v := ""
				return p.set(path, &v, true)
			}
			return nil
		}
	case nil:
		return p.set(path, nil, false)
	case string:
		return p.set(path, &value, false)
	case json.Number:
		v := value.String()
		return p.set(path, &v, false)
	case bool:
		v := "False"
		if value {
			v = "True"
		}
		return p.set(path, &v, false)
	}
	return errors.New("unsupported configuration token")
}

// Strip comments and trailing commas while preserving string contents. JSON
// syntax, escape validity and duplicate scalar paths are validated separately.
func cleanJSON(raw []byte) ([]byte, error) {
	out := append([]byte(nil), raw...)
	inString := false
	for i := 0; i < len(out); i++ {
		if inString {
			if out[i] == '\\' {
				if i+1 < len(out) && out[i+1] == 'u' {
					if i+6 <= len(out) {
						u, err := strconv.ParseUint(string(out[i+2:i+6]), 16, 16)
						if err == nil && u >= 0xd800 && u <= 0xdfff {
							if u > 0xdbff || i+12 > len(out) || out[i+6] != '\\' || out[i+7] != 'u' {
								return nil, errors.New("unpaired Unicode surrogate")
							}
							low, e := strconv.ParseUint(string(out[i+8:i+12]), 16, 16)
							if e != nil || low < 0xdc00 || low > 0xdfff {
								return nil, errors.New("unpaired Unicode surrogate")
							}
							i += 11
							continue
						}
					}
				}
				i++
				continue
			}
			if out[i] == '"' {
				inString = false
			}
			continue
		}
		if out[i] == '"' {
			inString = true
			continue
		}
		if out[i] == '/' && i+1 < len(out) {
			if out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i += 2
				for i < len(out) && out[i] != '\n' && out[i] != '\r' {
					out[i] = ' '
					i++
				}
				i--
				continue
			}
			if out[i+1] == '*' {
				out[i], out[i+1] = ' ', ' '
				i += 2
				closed := false
				for i+1 < len(out) {
					if out[i] == '*' && out[i+1] == '/' {
						out[i], out[i+1] = ' ', ' '
						i++
						closed = true
						break
					}
					out[i] = ' '
					i++
				}
				if !closed {
					return nil, errors.New("unterminated JSON comment")
				}
			}
		}
	}
	inString = false
	for i := 0; i < len(out); i++ {
		if inString {
			if out[i] == '\\' {
				i++
				continue
			}
			if out[i] == '"' {
				inString = false
			}
			continue
		}
		if out[i] == '"' {
			inString = true
			continue
		}
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\r' || out[j] == '\n') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				out[i] = ' '
			}
		}
	}
	return out, nil
}
