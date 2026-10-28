package slack

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// The Block Kit types the sink sends. Each message is one attachment, whose
// bar color shows the state, holding blocks.

// Block, element and text object types.
const (
	blockSection = "section"
	blockContext = "context"
	blockActions = "actions"
	elemButton   = "button"
	textMrkdwn   = "mrkdwn"
	textPlain    = "plain_text"
)

// Button styles.
const (
	stylePrimary = "primary"
	styleDanger  = "danger"
)

// attachment is a colored bar with blocks inside it.
type attachment struct {
	Color  string  `json:"color"`
	Blocks []block `json:"blocks"`
}

// block is a section, context or actions block. Slack calls the parts of
// a context block and of an actions block both "elements"; Elements holds
// the first and Buttons the second.
type block struct {
	Type     string
	BlockID  string
	Text     *textObject
	Fields   []textObject
	Elements []textObject
	Buttons  []button
}

// wireBlock is a block as Slack reads it.
type wireBlock struct {
	Type     string          `json:"type"`
	BlockID  string          `json:"block_id,omitempty"`
	Text     *textObject     `json:"text,omitempty"`
	Fields   []textObject    `json:"fields,omitempty"`
	Elements json.RawMessage `json:"elements,omitempty"`
}

// MarshalJSON writes the block as Slack reads it, with Buttons as the
// elements of an actions block.
func (b block) MarshalJSON() ([]byte, error) {
	w := wireBlock{Type: b.Type, BlockID: b.BlockID, Text: b.Text, Fields: b.Fields}

	var (
		elements []byte
		err      error
	)

	switch {
	case b.Type == blockActions:
		elements, err = marshal(b.Buttons)
	case len(b.Elements) > 0:
		elements, err = marshal(b.Elements)
	}

	if err != nil {
		return nil, fmt.Errorf("encode the elements of a %s block: %w", b.Type, err)
	}

	w.Elements = elements

	out, err := marshal(w)
	if err != nil {
		return nil, fmt.Errorf("encode a %s block: %w", b.Type, err)
	}

	return out, nil
}

// UnmarshalJSON reads a block that MarshalJSON wrote.
func (b *block) UnmarshalJSON(data []byte) error {
	var w wireBlock
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("decode a block: %w", err)
	}

	*b = block{Type: w.Type, BlockID: w.BlockID, Text: w.Text, Fields: w.Fields}

	if len(w.Elements) == 0 {
		return nil
	}

	target := any(&b.Elements)
	if w.Type == blockActions {
		target = &b.Buttons
	}

	if err := json.Unmarshal(w.Elements, target); err != nil {
		return fmt.Errorf("decode the elements of a %s block: %w", w.Type, err)
	}

	return nil
}

// textObject is mrkdwn text. Verbatim is always true, so Slack does not
// turn names or bare URLs in it into mentions or links.
type textObject struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Verbatim bool   `json:"verbatim"`
}

// plainText is a plain text object, as buttons and dialogs use.
type plainText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji"`
}

// button is a button in an actions block. Value carries the investigation
// ID back with the click.
type button struct {
	Type     string         `json:"type"`
	ActionID string         `json:"action_id"`
	Text     plainText      `json:"text"`
	Value    string         `json:"value,omitempty"`
	Style    string         `json:"style,omitempty"`
	Confirm  *confirmDialog `json:"confirm,omitempty"`
}

// confirmDialog asks before a button's action runs.
type confirmDialog struct {
	Title   plainText `json:"title"`
	Text    plainText `json:"text"`
	Confirm plainText `json:"confirm"`
	Deny    plainText `json:"deny"`
	Style   string    `json:"style,omitempty"`
}

// mrkdwn returns a text object for text, which must already be escaped.
func mrkdwn(text string) textObject {
	return textObject{Type: textMrkdwn, Text: text, Verbatim: true}
}

// plain returns a plain text object. Slack shows it as it is, so it needs
// no escaping.
func plain(text string) plainText {
	return plainText{Type: textPlain, Text: text, Emoji: true}
}

// marshal encodes v without escaping <, > and &, so the encoder that
// calls MarshalJSON decides, as it does for every other field.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
