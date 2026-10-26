package config

import (
	"encoding"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// maxDecodeDepth limits how deeply nested a config value may be, so an alias
// that refers to itself cannot recurse forever.
const maxDecodeDepth = 64

// YAML tags the decoder looks at.
const (
	nullTag  = "!!null"
	mergeTag = "!!merge"
)

var (
	durationType        = reflect.TypeFor[time.Duration]()
	nodeType            = reflect.TypeFor[yaml.Node]()
	yamlUnmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

	// plainKeyPattern matches map keys that a path can show after a dot.
	// Other keys are shown quoted in brackets, for example prices["a.b"].
	plainKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

	// yamlLinePrefix matches the "line 3: " the YAML library puts before
	// each of its messages.
	yamlLinePrefix = regexp.MustCompile(`(?m)^\s*line \d+: `)
)

// decoder decodes YAML nodes into Go values strictly: an unknown key is an
// error. It records every problem with its field path instead of stopping at
// the first one, and remembers the paths whose values it could not decode.
type decoder struct {
	errs   *errorList
	failed []string
	depth  int
}

// nodeDecoder is implemented by types that decode themselves, such as
// plugin sections, which keep most of their node for the plugin.
type nodeDecoder interface {
	decodeNode(d *decoder, node *yaml.Node, path string)
}

// defaulter is implemented by config structs that have default values. The
// decoder calls setDefaults before it decodes a mapping into the struct, so
// keys that are not set keep their defaults.
type defaulter interface {
	setDefaults()
}

// pair is one key and value of a mapping, after merge keys are applied.
type pair struct {
	key   string
	value *yaml.Node
}

// fieldSet describes how a struct's fields map to YAML keys.
type fieldSet struct {
	byKey     map[string][]int
	keys      []string
	inlineMap []int
}

// decode decodes node into v, which must be settable.
func (d *decoder) decode(node *yaml.Node, v reflect.Value, path string) {
	node = resolve(node)
	if node == nil || isNull(node) {
		return
	}

	if d.depth >= maxDecodeDepth {
		d.fail(path, "nested more than %d levels deep", maxDecodeDepth)

		return
	}

	d.depth++
	defer func() { d.depth-- }()

	if nd, ok := v.Addr().Interface().(nodeDecoder); ok {
		nd.decodeNode(d, node, path)

		return
	}

	if v.Kind() == reflect.Pointer && !isLeaf(v.Type()) {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}

		d.decode(node, v.Elem(), path)

		return
	}

	switch {
	case isLeaf(v.Type()):
		d.decodeLeaf(node, v, path)
	case v.Kind() == reflect.Struct:
		d.decodeStruct(node, v, path)
	case v.Kind() == reflect.Slice:
		d.decodeSlice(node, v, path)
	case v.Kind() == reflect.Map:
		d.decodeMap(node, v, path)
	default:
		d.decodeLeaf(node, v, path)
	}
}

// decodeStruct decodes a mapping into a struct, field by field.
func (d *decoder) decodeStruct(node *yaml.Node, v reflect.Value, path string) {
	if df, ok := v.Addr().Interface().(defaulter); ok {
		df.setDefaults()
	}

	if node.Kind != yaml.MappingNode {
		d.fail(path, "expected a mapping, got %s", describe(node))

		return
	}

	fields := structFields(v.Type())
	for _, p := range d.pairs(node, path) {
		keyPath := joinPath(path, p.key)

		index, ok := fields.byKey[p.key]
		if ok {
			d.decode(p.value, v.FieldByIndex(index), keyPath)

			continue
		}

		if fields.inlineMap != nil {
			d.decodeMapEntry(p, v.FieldByIndex(fields.inlineMap), keyPath)

			continue
		}

		d.fail(keyPath, "unknown key%s", didYouMean(p.key, fields.keys))
	}
}

// decodeSlice decodes a sequence into a slice.
func (d *decoder) decodeSlice(node *yaml.Node, v reflect.Value, path string) {
	if node.Kind != yaml.SequenceNode {
		d.fail(path, "expected a list, got %s", describe(node))

		return
	}

	out := reflect.MakeSlice(v.Type(), len(node.Content), len(node.Content))
	for i, item := range node.Content {
		d.decode(item, out.Index(i), fmt.Sprintf("%s[%d]", path, i))
	}

	v.Set(out)
}

// decodeMap decodes a mapping into a map.
func (d *decoder) decodeMap(node *yaml.Node, v reflect.Value, path string) {
	if node.Kind != yaml.MappingNode {
		d.fail(path, "expected a mapping, got %s", describe(node))

		return
	}

	for _, p := range d.pairs(node, path) {
		d.decodeMapEntry(p, v, joinPath(path, p.key))
	}
}

// decodeMapEntry decodes one key and value into the map m, creating m when
// it is nil.
func (d *decoder) decodeMapEntry(p pair, m reflect.Value, path string) {
	if m.IsNil() {
		m.Set(reflect.MakeMap(m.Type()))
	}

	key := reflect.New(m.Type().Key()).Elem()
	if key.Kind() != reflect.String {
		d.fail(path, "map keys of type %s are not supported", key.Type())

		return
	}

	key.SetString(p.key)

	value := reflect.New(m.Type().Elem()).Elem()
	d.decode(p.value, value, path)
	m.SetMapIndex(key, value)
}

// decodeLeaf decodes a value the YAML library handles itself: scalars,
// durations, interfaces and types with their own unmarshal methods.
func (d *decoder) decodeLeaf(node *yaml.Node, v reflect.Value, path string) {
	if err := node.Decode(v.Addr().Interface()); err != nil {
		d.fail(path, "%s", leafMessage(node, v.Type(), err))
	}
}

// pairs returns the keys and values of a mapping in file order, with merge
// keys (<<) applied: keys set in the mapping win over merged ones, and
// earlier merged mappings win over later ones. A key set twice is an error.
func (d *decoder) pairs(node *yaml.Node, path string) []pair {
	var own, merged []pair

	seen := map[string]bool{}

	for i := 0; i+1 < len(node.Content); i += 2 {
		k, value := node.Content[i], node.Content[i+1]
		if isMergeKey(k) {
			merged = append(merged, d.mergeSources(value, path)...)

			continue
		}

		if k.Kind != yaml.ScalarNode {
			d.fail(path, "keys must be plain values, got %s", describe(k))

			continue
		}

		if seen[k.Value] {
			d.fail(joinPath(path, k.Value), "set more than once")

			continue
		}

		seen[k.Value] = true
		own = append(own, pair{key: k.Value, value: value})
	}

	for _, p := range merged {
		if !seen[p.key] {
			seen[p.key] = true
			own = append(own, p)
		}
	}

	return own
}

// mergeSources returns the pairs a merge key brings in: one mapping, or a
// list of mappings.
func (d *decoder) mergeSources(node *yaml.Node, path string) []pair {
	node = resolve(node)

	switch node.Kind {
	case yaml.MappingNode:
		return d.pairs(node, path)
	case yaml.SequenceNode:
		var out []pair
		for _, item := range node.Content {
			if item = resolve(item); item.Kind != yaml.MappingNode {
				d.fail(path, "a merge key (<<) needs mappings, got %s", describe(item))

				continue
			}

			out = append(out, d.pairs(item, path)...)
		}

		return out
	default:
		d.fail(path, "a merge key (<<) needs a mapping, got %s", describe(node))

		return nil
	}
}

// fail records a problem at path and remembers that the value at path could
// not be decoded, so later checks do not report it again.
func (d *decoder) fail(path, format string, args ...any) {
	d.errs.add(path, format, args...)
	d.failed = append(d.failed, path)
}

// underFailed reports whether path is a path the decoder failed on, or
// lies inside one.
func (d *decoder) underFailed(path string) bool {
	return slices.ContainsFunc(d.failed, func(failed string) bool {
		return isPathWithin(path, failed)
	})
}

// structFields lists the YAML keys of a struct type. A field's key is its
// yaml tag name, or its Go name in lower case as the YAML library does.
// Fields tagged ",inline" add their own fields, or collect unknown keys when
// they are maps.
func structFields(t reflect.Type) fieldSet {
	fs := fieldSet{byKey: map[string][]int{}}

	for i := range t.NumField() {
		f := t.Field(i)

		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		inlineEmbedded := f.Anonymous && isInline(f)
		if name == "-" || (!f.IsExported() && !inlineEmbedded) {
			continue
		}

		if isInline(f) {
			switch f.Type.Kind() {
			case reflect.Struct:
				inner := structFields(f.Type)
				for _, key := range inner.keys {
					fs.add(key, append([]int{i}, inner.byKey[key]...))
				}

				if inner.inlineMap != nil {
					fs.inlineMap = append([]int{i}, inner.inlineMap...)
				}
			case reflect.Map:
				fs.inlineMap = []int{i}
			}

			continue
		}

		if name == "" {
			name = strings.ToLower(f.Name)
		}

		fs.add(name, []int{i})
	}

	return fs
}

// isInline reports whether a struct field is tagged ",inline".
func isInline(f reflect.StructField) bool {
	_, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")

	return slices.Contains(strings.Split(opts, ","), "inline")
}

func (fs *fieldSet) add(key string, index []int) {
	fs.byKey[key] = index
	fs.keys = append(fs.keys, key)
}

// resolve follows aliases and unwraps a document to its content.
func resolve(node *yaml.Node) *yaml.Node {
	for node != nil {
		switch {
		case node.Kind == yaml.AliasNode:
			node = node.Alias
		case node.Kind == yaml.DocumentNode && len(node.Content) > 0:
			node = node.Content[0]
		case node.Kind == yaml.DocumentNode, node.Kind == 0:
			return nil
		default:
			return node
		}
	}

	return nil
}

// isLeaf reports whether values of type t are decoded by the YAML library
// rather than field by field.
func isLeaf(t reflect.Type) bool {
	if t == nodeType || t.Kind() == reflect.Interface {
		return true
	}

	pt := reflect.PointerTo(t)

	return pt.Implements(yamlUnmarshalerType) || pt.Implements(textUnmarshalerType)
}

func isNull(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == nullTag
}

func isMergeKey(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.Value == "<<" && node.ShortTag() == mergeTag
}

// leafMessage explains why a value could not be decoded into type t.
func leafMessage(node *yaml.Node, t reflect.Type, err error) string {
	want := describeType(t)
	if want == "" {
		return "invalid value: " + cleanYAMLError(err)
	}

	if node.Kind != yaml.ScalarNode {
		return fmt.Sprintf("expected %s, got %s", want, describe(node))
	}

	return fmt.Sprintf("expected %s, got %q", want, node.Value)
}

// describeType names what a value of type t looks like in YAML, or returns
// "" for types with their own unmarshal methods.
func describeType(t reflect.Type) string {
	if t == durationType {
		return "a duration such as 30s or 10m"
	}

	if isLeaf(t) {
		return ""
	}

	switch t.Kind() {
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "a string"
	default:
		return ""
	}
}

// cleanYAMLError removes the YAML library's prefixes and line numbers from
// err, because config errors name the field path instead.
func cleanYAMLError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n")

	return yamlLinePrefix.ReplaceAllString(strings.TrimSpace(msg), "")
}

// describe names the kind of a node for error messages.
func describe(node *yaml.Node) string {
	switch node.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	default:
		return strconv.Quote(node.Value)
	}
}

// joinPath adds a key to a field path.
func joinPath(path, key string) string {
	if !plainKeyPattern.MatchString(key) {
		return path + "[" + strconv.Quote(key) + "]"
	}

	if path == "" {
		return key
	}

	return path + "." + key
}

// isPathWithin reports whether path is parent or a field or item inside it.
func isPathWithin(path, parent string) bool {
	rest, ok := strings.CutPrefix(path, parent)

	return ok && (rest == "" || parent == "" || rest[0] == '.' || rest[0] == '[')
}
