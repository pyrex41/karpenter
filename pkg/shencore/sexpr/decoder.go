/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Adapted from shen_split/go/sexpr (decoder.go). See package doc for provenance.

package sexpr

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Decode parses a single value from data. Any non-whitespace bytes after the
// value are an error — this is a wire-frame parser, not a reader loop.
func Decode(data []byte) (Value, error) {
	d := &decoder{src: data}
	d.skipWS()
	v, err := d.readValue()
	if err != nil {
		return nil, err
	}
	d.skipWS()
	if d.pos != len(d.src) {
		return nil, fmt.Errorf("sexpr: trailing bytes at offset %d", d.pos)
	}
	return v, nil
}

type decoder struct {
	src []byte
	pos int
}

func (d *decoder) skipWS() {
	for d.pos < len(d.src) {
		c := d.src[d.pos]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return
		}
		d.pos++
	}
}

func (d *decoder) readValue() (Value, error) {
	if d.pos >= len(d.src) {
		return nil, errors.New("sexpr: unexpected EOF")
	}
	switch d.src[d.pos] {
	case '(':
		return d.readList()
	case '"':
		return d.readString()
	}
	return d.readAtom()
}

func (d *decoder) readList() (List, error) {
	d.pos++ // consume '('
	out := List{}
	for {
		d.skipWS()
		if d.pos >= len(d.src) {
			return nil, errors.New("sexpr: unterminated list")
		}
		if d.src[d.pos] == ')' {
			d.pos++
			return out, nil
		}
		v, err := d.readValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func (d *decoder) readString() (String, error) {
	d.pos++ // consume opening '"'
	var b strings.Builder
	for d.pos < len(d.src) {
		c := d.src[d.pos]
		if c == '"' {
			d.pos++
			return String(b.String()), nil
		}
		if c == '\\' {
			d.pos++
			if d.pos >= len(d.src) {
				return "", errors.New("sexpr: trailing backslash in string")
			}
			esc := d.src[d.pos]
			if esc != '"' && esc != '\\' {
				return "", fmt.Errorf("sexpr: unknown string escape \\%c", esc)
			}
			b.WriteByte(esc)
			d.pos++
			continue
		}
		b.WriteByte(c)
		d.pos++
	}
	return "", errors.New("sexpr: unterminated string")
}

// readAtom consumes a bare (non-list, non-string) token and classifies it as
// Int, empty List (NIL), or Symbol. Vbar-quoted symbol names are handled
// out-of-band because their bodies may contain '(' / ')' / space after
// backslash-escaping.
func (d *decoder) readAtom() (Value, error) {
	if bytes.HasPrefix(d.src[d.pos:], []byte("SHEN::|")) {
		return d.readVbarSymbol()
	}
	start := d.pos
	for d.pos < len(d.src) {
		c := d.src[d.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '(' || c == ')' {
			break
		}
		d.pos++
	}
	tok := string(d.src[start:d.pos])
	if tok == "" {
		return nil, fmt.Errorf("sexpr: empty atom at offset %d", start)
	}
	return classifyAtom(tok)
}

func (d *decoder) readVbarSymbol() (Symbol, error) {
	d.pos += len("SHEN::|")
	var b strings.Builder
	for d.pos < len(d.src) {
		c := d.src[d.pos]
		if c == '\\' {
			d.pos++
			if d.pos >= len(d.src) {
				return "", errors.New("sexpr: trailing backslash in vbar symbol")
			}
			b.WriteByte(d.src[d.pos])
			d.pos++
			continue
		}
		if c == '|' {
			d.pos++
			return Symbol(b.String()), nil
		}
		b.WriteByte(c)
		d.pos++
	}
	return "", errors.New("sexpr: unterminated vbar symbol")
}

func classifyAtom(tok string) (Value, error) {
	if tok == "NIL" {
		return List{}, nil
	}
	if isIntLit(tok) {
		n, err := strconv.ParseInt(tok, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("sexpr: parse int %q: %w", tok, err)
		}
		return Int(n), nil
	}
	if strings.HasPrefix(tok, "SHEN::") {
		return Symbol(tok[len("SHEN::"):]), nil
	}
	return nil, fmt.Errorf("sexpr: unrecognized atom %q (expected integer, NIL, or SHEN::symbol)", tok)
}

func isIntLit(tok string) bool {
	if tok == "" {
		return false
	}
	i := 0
	if tok[0] == '-' {
		if len(tok) == 1 {
			return false
		}
		i = 1
	}
	for ; i < len(tok); i++ {
		c := tok[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
