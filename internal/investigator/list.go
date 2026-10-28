package investigator

// MaxListTitleChars is the most characters of an investigation's alert
// title that the list stores. The backfill of old rows cuts to the same
// length.
const MaxListTitleChars = 200

// ListTitle returns the title of the investigation list for the event that
// opened the group: the event's title, else its alert name, else the start
// of its text, cut to MaxListTitleChars characters. It is empty when the
// event has none of them.
func ListTitle(title, alertName, body string) string {
	text := title

	if text == "" {
		text = alertName
	}

	if text == "" {
		text = body
	}

	runes := []rune(text)
	if len(runes) > MaxListTitleChars {
		return string(runes[:MaxListTitleChars])
	}

	return text
}
