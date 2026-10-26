package slack

// The Block Kit types the sink sends. Each message is one attachment, whose
// bar color shows the state, holding blocks.

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
