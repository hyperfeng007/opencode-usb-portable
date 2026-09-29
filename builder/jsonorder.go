package main

// Minimal JSON model that preserves object key order and number formatting,
// so an ASAR header can be read, modified and written back byte-identically.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	kindObject = iota
	kindArray
	kindString
	kindNumber
	kindBool
	kindNull
)

type node struct {
	kind int
	keys []string
	obj  map[string]*node
	arr  []*node
	str  string
	num  json.Number
	b    bool
}

func newObject() *node { return &node{kind: kindObject, obj: map[string]*node{}} }
func newArray() *node  { return &node{kind: kindArray} }

func (n *node) set(key string, value *node) {
	if _, exists := n.obj[key]; !exists {
		n.keys = append(n.keys, key)
	}
	n.obj[key] = value
}

func (n *node) get(key string) (*node, bool) {
	v, ok := n.obj[key]
	return v, ok
}

func (n *node) getString(key string) (string, bool) {
	v, ok := n.obj[key]
	if !ok || v.kind != kindString {
		return "", false
	}
	return v.str, true
}

func (n *node) append(value *node) { n.arr = append(n.arr, value) }

// ---------------------------------------------------------------------------
// decoding
// ---------------------------------------------------------------------------
func decodeJSON(data []byte) (*node, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	return decodeValue(dec)
}

func decodeValue(dec *json.Decoder) (*node, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := newObject()
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				child, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				object.set(key, child)
			}
			if _, err := dec.Token(); err != nil { // closing '}'
				return nil, err
			}
			return object, nil
		case '[':
			array := newArray()
			for dec.More() {
				child, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				array.append(child)
			}
			if _, err := dec.Token(); err != nil { // closing ']'
				return nil, err
			}
			return array, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", value)
	case string:
		return &node{kind: kindString, str: value}, nil
	case json.Number:
		return &node{kind: kindNumber, num: value}, nil
	case bool:
		return &node{kind: kindBool, b: value}, nil
	case nil:
		return &node{kind: kindNull}, nil
	}
	return nil, fmt.Errorf("unsupported JSON token %T", token)
}

// ---------------------------------------------------------------------------
// encoding (matches JavaScript's JSON.stringify escaping)
// ---------------------------------------------------------------------------
func (n *node) encode(sb *strings.Builder) {
	switch n.kind {
	case kindObject:
		sb.WriteByte('{')
		for i, key := range n.keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeJSONString(sb, key)
			sb.WriteByte(':')
			n.obj[key].encode(sb)
		}
		sb.WriteByte('}')
	case kindArray:
		sb.WriteByte('[')
		for i, item := range n.arr {
			if i > 0 {
				sb.WriteByte(',')
			}
			item.encode(sb)
		}
		sb.WriteByte(']')
	case kindString:
		writeJSONString(sb, n.str)
	case kindNumber:
		sb.WriteString(n.num.String())
	case kindBool:
		if n.b {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case kindNull:
		sb.WriteString("null")
	}
}

func (n *node) marshal() string {
	var sb strings.Builder
	n.encode(&sb)
	return sb.String()
}

func writeJSONString(sb *strings.Builder, value string) {
	sb.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			if r < 0x20 || r == utf8.RuneError {
				sb.WriteString(`\u`)
				sb.WriteString(fmt.Sprintf("%04x", r))
				continue
			}
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
}

// sortedKeys is handy when reporting differences.
func sortedKeys(m map[string]*node) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

var _ = strconv.Itoa
