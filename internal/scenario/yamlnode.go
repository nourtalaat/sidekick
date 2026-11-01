package scenario

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// indent is the number of spaces the scenario files indent by.
const indent = 2

// jsonNode converts JSON to a YAML node that keeps the order of the keys and
// the text of the numbers.
func jsonNode(raw json.RawMessage) (*yaml.Node, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	node, err := nodeFrom(dec)
	if err != nil {
		return nil, fmt.Errorf("read the payload as JSON: %w", err)
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("read the payload as JSON: more than one value")
	}

	return node, nil
}

// nodeFrom reads the next JSON value.
func nodeFrom(dec *json.Decoder) (*yaml.Node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("read token: %w", err)
	}

	switch t := tok.(type) {
	case json.Delim:
		return containerFrom(dec, t)
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}, nil
	case json.Number:
		tag := "!!float"
		if _, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
			tag = "!!int"
		}

		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: t.String()}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}

	return nil, fmt.Errorf("unexpected token %v", tok)
}

// containerFrom reads an object or an array whose opening delimiter was read.
func containerFrom(dec *json.Decoder, open json.Delim) (*yaml.Node, error) {
	if open == '[' {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

		for dec.More() {
			item, err := nodeFrom(dec)
			if err != nil {
				return nil, fmt.Errorf("read an array item: %w", err)
			}

			seq.Content = append(seq.Content, item)
		}

		if _, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("read the end of an array: %w", err)
		}

		return seq, nil
	}

	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("read a key: %w", err)
		}

		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("object key %v is not a string", keyTok)
		}

		value, err := nodeFrom(dec)
		if err != nil {
			return nil, fmt.Errorf("read the value of %s: %w", key, err)
		}

		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	}

	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("read the end of an object: %w", err)
	}

	return m, nil
}

// encodeYAML writes a node as YAML.
func encodeYAML(node *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)

	if err := enc.Encode(node); err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}

	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close the YAML encoder: %w", err)
	}

	return buf.Bytes(), nil
}

// content returns the mapping at the top of a node, looking through a
// document node.
func content(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}

	return n
}

// child returns the value under key in a mapping node, or nil.
func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}

	m = content(m)
	if m.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}

	return nil
}

// setString sets the value of the string under key in a mapping node and
// reports whether the key was there.
func setString(m *yaml.Node, key, value string) bool {
	n := child(m, key)
	if n == nil {
		return false
	}

	n.Value = value
	n.Tag = "!!str"
	n.Style = 0

	return true
}

// appendEncoded adds key with the encoding of value to a mapping node.
func appendEncoded(m *yaml.Node, key string, value any) error {
	m = content(m)

	var v yaml.Node
	if err := v.Encode(value); err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}

	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &v)

	return nil
}

// hasPrefixValue reports whether the node holds a string that starts with
// prefix.
func hasPrefixValue(n *yaml.Node, prefix string) bool {
	return n.Kind == yaml.ScalarNode && strings.HasPrefix(n.Value, prefix)
}

// resetStyles clears how each scalar was written, so the encoder picks the
// plain style when the text allows it and quotes it when it must, whatever
// the node was read from.
func resetStyles(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode {
		n.Style = 0
	}

	for _, c := range n.Content {
		resetStyles(c)
	}
}
