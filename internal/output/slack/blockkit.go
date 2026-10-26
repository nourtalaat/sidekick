package slack

// The Block Kit types the sink sends. Each message is one attachment, whose
// bar color shows the state, holding blocks.

// Block and text object types.
const (
	blockSection = "section"
	blockContext = "context"
	textMrkdwn   = "mrkdwn"
)

// attachment is a colored bar with blocks inside it.
type attachment struct {
	Color  string  `json:"color"`
	Blocks []block `json:"blocks"`
}

// block is a section or context block.
type block struct {
	Type     string       `json:"type"`
	Text     *textObject  `json:"text,omitempty"`
	Fields   []textObject `json:"fields,omitempty"`
	Elements []textObject `json:"elements,omitempty"`
}

// textObject is mrkdwn text. Verbatim is always true, so Slack does not
// turn names or bare URLs in it into mentions or links.
type textObject struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Verbatim bool   `json:"verbatim"`
}

// mrkdwn returns a text object for text, which must already be escaped.
func mrkdwn(text string) textObject {
	return textObject{Type: textMrkdwn, Text: text, Verbatim: true}
}
