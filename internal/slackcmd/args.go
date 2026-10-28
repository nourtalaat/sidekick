package slackcmd

import (
	"regexp"
	"strings"
	"unicode"
)

// maxNameLength is the longest rule name the config allows.
const maxNameLength = 24

var (
	// channelMention is a channel as Slack sends it in command text:
	// <#C01ABCDEF|alerts>, or <#C01ABCDEF> without its name.
	channelMention = regexp.MustCompile(`^<#([CG][A-Z0-9]{2,})(?:\|([^>]*))?>$`)
	// channelIDPattern is a Slack channel ID, such as C01ABCDEF.
	channelIDPattern = regexp.MustCompile(`^[CG][A-Z0-9]{2,}$`)
	// botIDPattern is a Slack bot ID, such as B01ABCDEF.
	botIDPattern = regexp.MustCompile(`^B[A-Z0-9]{2,}$`)
	// notNameChars are the characters a rule name cannot hold.
	notNameChars = regexp.MustCompile(`[^a-z0-9-]+`)
)

// splitArgs splits command text into words. Text in double quotes, plain
// or the curly ones Slack may turn them into, is one word without its
// quotes, so a pattern can hold spaces.
func splitArgs(text string) []string {
	var (
		args    []string
		current strings.Builder
		quoted  bool
		started bool
	)

	for _, r := range text {
		switch {
		case r == '"' || r == '“' || r == '”':
			quoted = !quoted
			started = true
		case unicode.IsSpace(r) && !quoted:
			if started {
				args = append(args, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}

	if started {
		args = append(args, current.String())
	}

	return args
}

// channelArg reads a channel from command text: a channel mention, a
// channel name with its "#", or a channel ID. It returns the channel as a
// listener names it, an ID when Slack sent one, and the channel's name
// without "#", or "" when it is not known.
func channelArg(arg string) (channel, name string) {
	if m := channelMention.FindStringSubmatch(arg); m != nil {
		return m[1], m[2]
	}

	if n, ok := strings.CutPrefix(arg, "#"); ok {
		return arg, n
	}

	return arg, ""
}

// listenerName makes a listener name from a channel name: lower case,
// with runs of other characters turned into "-", cut to the longest name
// allowed. It returns "" when nothing usable is left.
func listenerName(channelName string) string {
	name := strings.Trim(notNameChars.ReplaceAllString(strings.ToLower(channelName), "-"), "-")
	name = strings.TrimLeft(name, "0123456789-")

	if len(name) > maxNameLength {
		name = strings.TrimRight(name[:maxNameLength], "-")
	}

	return name
}

// option splits an option such as bot:SigNoz into its key and value.
func option(arg string) (key, value string, ok bool) {
	key, value, ok = strings.Cut(arg, ":")
	if !ok || key == "" {
		return "", "", false
	}

	return key, value, true
}

// listValue splits a comma-separated value, dropping empty items. "none"
// is an empty list.
func listValue(value string) []string {
	if value == noneValue {
		return nil
	}

	var out []string

	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}

	return out
}

// escape makes text safe to show in a Slack message, where &, < and >
// have meanings of their own.
func escape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

// code shows text as inline code in a Slack message.
func code(text string) string {
	return "`" + strings.ReplaceAll(escape(text), "`", "'") + "`"
}
