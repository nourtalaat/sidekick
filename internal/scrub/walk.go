package scrub

import (
	"strconv"

	"go.yaml.in/yaml/v3"
)

// scalar is one string, or one number under a key that names a kind of
// value, found in a document.
type scalar struct {
	node *yaml.Node
	// path is the field, for example "event.payload.alerts[0].labels.host".
	path string
	// key is set for a mapping key.
	key bool
	// kind is the kind of value the nearest key says the scalar is, or "".
	kind Kind
}

// walk calls fn for every string in doc, mapping keys included, and for
// every number under a key that names a kind of value, in document order.
func walk(doc *yaml.Node, fn func(scalar)) {
	walkNode(doc, "", nil, fn)
}

// walkNode visits n, whose path is path and whose enclosing keys are keys.
func walkNode(n *yaml.Node, path string, keys []string, fn func(scalar)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkNode(c, path, keys, fn)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			child := joinPath(path, key.Value)

			if key.Kind == yaml.ScalarNode {
				fn(scalar{node: key, path: child, key: true})
			}

			walkNode(value, child, append(keys[:len(keys):len(keys)], key.Value), fn)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walkNode(c, path+"["+strconv.Itoa(i)+"]", keys, fn)
		}
	case yaml.ScalarNode:
		kind := kindOfEnclosingKey(keys)

		switch n.Tag {
		case "!!str":
			fn(scalar{node: n, path: path, kind: kind})
		case "!!int", "!!float":
			if kind != "" {
				fn(scalar{node: n, path: path, kind: kind})
			}
		}
	case yaml.AliasNode:
	}
}

// kindOfEnclosingKey returns the kind the nearest key that is not a matcher
// operator says, or "".
func kindOfEnclosingKey(keys []string) Kind {
	for i := len(keys) - 1; i >= 0; i-- {
		if isPassthrough(keys[i]) {
			continue
		}

		return kindOfKey(keys[i])
	}

	return ""
}

// joinPath adds a key to a path.
func joinPath(path, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}
