package prometheusrule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// ruleFields returns the fields of a rule that the action can change, as
// they compare between a write and a read. The API server stores a field
// as it was sent, apart from dropping the fields the CRD does not know, so
// the only change is for expr, which the CRD lets be a number or a text:
// a number is turned into the text of the number. Fields that are null or
// not editable are left out.
func ruleFields(rule json.RawMessage) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(rule, &all); err != nil {
		return nil, fmt.Errorf("decode the rule: %w", err)
	}

	fields := make(map[string]json.RawMessage, len(editableRoots))

	for _, key := range editableRoots {
		value, ok := all[key]
		if !ok || isNull(value) {
			continue
		}

		if key == "expr" {
			value = exprText(value)
		}

		fields[key] = value
	}

	return fields, nil
}

// exprText returns an expression written as a JSON number as the JSON text
// of that number, and any other value as it is.
func exprText(value json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(value)

	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return value
	}

	return json.RawMessage(strconv.Quote(string(trimmed)))
}

// isNull reports whether value is JSON null.
func isNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
