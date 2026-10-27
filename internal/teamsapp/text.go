package teamsapp

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The card action types whose links are taken, and the keys that hold
// their URLs.
const (
	actionOpenURL    = "Action.OpenUrl" // Adaptive Cards
	buttonOpenURL    = "openUrl"        // hero and thumbnail cards
	connectorOpenURI = "OpenUri"        // Office 365 connector cards
)

// cardTextKeys are the keys of card objects whose strings are message
// text, in the order a card shows them.
var cardTextKeys = []string{"title", "activityTitle", "subtitle", "activitySubtitle", "text", "activityText", "value"}

// actionKeys are the keys of card objects that hold buttons.
var actionKeys = []string{"actions", "buttons", "potentialAction"}

// blockElements end a line in HTML text.
var blockElements = []atom.Atom{atom.Br, atom.P, atom.Div, atom.Li, atom.Tr, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Blockquote, atom.Pre}

// flattener collects the text parts of a message.
type flattener struct {
	parts []string
}

// String returns the parts, one per line.
func (f *flattener) String() string {
	return strings.Join(f.parts, "\n")
}

// addHTML adds the text of s, which may hold HTML, one part per line.
func (f *flattener) addHTML(s string) {
	for line := range strings.SplitSeq(htmlText(s), "\n") {
		f.add(line)
	}
}

// addAttachment adds the text of a card, or of the message's HTML.
func (f *flattener) addAttachment(att attachment) {
	text, card := decodeContent(att)
	if text != "" {
		f.addHTML(text)

		return
	}

	if card != nil && isCard(att.ContentType) {
		f.addCard(card)
	}
}

// addCard adds every string under one of cardTextKeys at any depth of a
// card.
func (f *flattener) addCard(v any) {
	switch v := v.(type) {
	case map[string]any:
		for _, key := range cardTextKeys {
			if s, ok := v[key].(string); ok {
				f.addHTML(s)
			}
		}

		for _, key := range actionsLast(slices.Sorted(maps.Keys(v))) {
			if _, isText := v[key].(string); isText {
				continue
			}

			f.addCard(v[key])
		}
	case []any:
		for _, child := range v {
			f.addCard(child)
		}
	}
}

// add appends s unless it is empty or already there.
func (f *flattener) add(s string) {
	s = strings.TrimSpace(s)
	if s == "" || slices.Contains(f.parts, s) {
		return
	}

	f.parts = append(f.parts, s)
}

// decodeContent returns an attachment's content: the HTML text, or a
// decoded card. Both are empty for content that does not decode.
func decodeContent(att attachment) (htmlText string, card any) {
	if len(att.Content) == 0 {
		return "", nil
	}

	var s string
	if err := json.Unmarshal(att.Content, &s); err == nil {
		if att.ContentType == contentHTML {
			return s, nil
		}

		if err := json.Unmarshal([]byte(s), &card); err != nil {
			return "", nil
		}

		return "", card
	}

	if err := json.Unmarshal(att.Content, &card); err != nil {
		return "", nil
	}

	return "", card
}

// attachmentLinks returns the links of an attachment: the hrefs of its
// HTML, or the URLs of its card's open URL actions.
func attachmentLinks(att attachment) []link {
	text, card := decodeContent(att)
	if text != "" {
		return htmlLinks(text)
	}

	if card == nil || !isCard(att.ContentType) {
		return nil
	}

	var out []link

	walkActions(card, &out)

	return out
}

// walkActions appends the links of the open URL actions in v.
func walkActions(v any, out *[]link) {
	switch v := v.(type) {
	case map[string]any:
		title, _ := v["title"].(string)

		switch {
		case v["type"] == actionOpenURL:
			url, _ := v["url"].(string)
			*out = append(*out, webLink(url, title))
		case v["type"] == buttonOpenURL:
			url, _ := v["value"].(string)
			*out = append(*out, webLink(url, title))
		case v["@type"] == connectorOpenURI:
			name, _ := v["name"].(string)
			if targets, ok := v["targets"].([]any); ok && len(targets) > 0 {
				if target, ok := targets[0].(map[string]any); ok {
					uri, _ := target["uri"].(string)
					*out = append(*out, webLink(uri, name))
				}
			}
		}

		for _, key := range slices.Sorted(maps.Keys(v)) {
			walkActions(v[key], out)
		}
	case []any:
		for _, child := range v {
			walkActions(child, out)
		}
	}
}

// htmlText returns the text of s, which may hold HTML: tags are left out,
// entities decoded, and block elements end lines.
func htmlText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return s
	}

	var b strings.Builder

	z := html.NewTokenizer(strings.NewReader(s))

	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.TextToken:
			b.Write(z.Text())
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			if slices.Contains(blockElements, atom.Lookup(name)) {
				b.WriteByte('\n')
			}
		default:
		}
	}
}

// htmlLinks returns the http and https links of the anchors in s, with
// their text as the title.
func htmlLinks(s string) []link {
	if !strings.Contains(s, "<a") && !strings.Contains(s, "<A") {
		return nil
	}

	var (
		out  []link
		open *link
		text strings.Builder
	)

	z := html.NewTokenizer(strings.NewReader(s))

	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken:
			if t := z.Token(); t.DataAtom == atom.A {
				for _, attr := range t.Attr {
					if attr.Key == "href" {
						open = &link{url: attr.Val}
						text.Reset()
					}
				}
			}
		case html.TextToken:
			if open != nil {
				text.Write(z.Text())
			}
		case html.EndTagToken:
			if t := z.Token(); t.DataAtom == atom.A && open != nil {
				out = append(out, webLink(open.url, strings.TrimSpace(text.String())))
				open = nil
			}
		default:
		}
	}
}

// webLink returns a link to url, or the zero link when url is not http or
// https, which leaves it out.
func webLink(url, title string) link {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return link{}
	}

	return link{url: url, title: strings.TrimSpace(title)}
}

// actionsLast moves the keys of a card's actions to the end of keys, since
// a card shows its body before its buttons.
func actionsLast(keys []string) []string {
	var actions []string

	keys = slices.DeleteFunc(keys, func(k string) bool {
		if slices.Contains(actionKeys, k) {
			actions = append(actions, k)

			return true
		}

		return false
	})

	return append(keys, actions...)
}

// isCard reports whether contentType is a card Sidekick reads.
func isCard(contentType string) bool {
	return slices.Contains([]string{contentAdaptiveCard, contentHeroCard, contentThumbnailCard, contentO365Connector}, contentType)
}
