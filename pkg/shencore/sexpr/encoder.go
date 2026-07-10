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

// Adapted from shen_split/go/sexpr (encoder.go). See package doc for provenance.

package sexpr

import (
	"fmt"
	"strconv"
	"strings"
)

// Encode produces the canonical byte sequence for v, byte-for-byte identical to
// the shen_split codec's output under with-standard-io-syntax + :readably t +
// :print-pretty nil.
func Encode(v Value) ([]byte, error) {
	var b strings.Builder
	if err := encodeTo(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func encodeTo(b *strings.Builder, v Value) error {
	switch x := v.(type) {
	case Int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
		return nil
	case String:
		return encodeString(b, string(x))
	case Symbol:
		return encodeSymbol(b, string(x))
	case List:
		return encodeList(b, x)
	}
	return fmt.Errorf("sexpr: unsupported value type %T", v)
}

func encodeString(b *strings.Builder, s string) error {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7F {
			return fmt.Errorf("sexpr: control byte 0x%02x in string (index %d); CL :readably printer would escape with #A(...) which this encoder does not reproduce", c, i)
		}
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return nil
}

// symbolNeedsVbar conservatively classifies a symbol name as requiring
// vbar-quoted printing. The rule approximates CL's default :upcase readtable:
// any char whose read would not re-produce the original name triggers vbars.
// Covers every canonical lowercase-kebab payload symbol.
func symbolNeedsVbar(name string) bool {
	if name == "" {
		return true
	}
	allDigits := true
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < '0' || c > '9' {
			allDigits = false
		}
		if c >= 'a' && c <= 'z' {
			return true
		}
		switch c {
		case '(', ')', '\'', '"', '\\', '|', ';', '#', ',', '`', ' ', '\t', '\n', '\r':
			return true
		}
	}
	return allDigits
}

func encodeSymbol(b *strings.Builder, name string) error {
	b.WriteString("SHEN::")
	if !symbolNeedsVbar(name) {
		b.WriteString(name)
		return nil
	}
	b.WriteByte('|')
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '|' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('|')
	return nil
}

func encodeList(b *strings.Builder, xs List) error {
	if len(xs) == 0 {
		b.WriteString("NIL")
		return nil
	}
	b.WriteByte('(')
	for i, x := range xs {
		if i > 0 {
			b.WriteByte(' ')
		}
		if err := encodeTo(b, x); err != nil {
			return err
		}
	}
	b.WriteByte(')')
	return nil
}
