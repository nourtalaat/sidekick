package lessons

// The limits on the lessons of one prompt, and what the config accepts.
const (
	// DefaultMaxPerPrompt is the most lessons a prompt shows when the
	// config does not say.
	DefaultMaxPerPrompt = 10
	// MaxPerPromptLimit is the most lessons the config may allow a prompt.
	MaxPerPromptLimit = 50
	// DefaultMaxTokens is the most tokens the lessons of a prompt may use
	// when the config does not say.
	DefaultMaxTokens = 1500
	// MinTokens and MaxTokensLimit bound what the config may allow. The
	// least fits one lesson of the longest text.
	MinTokens      = 200
	MaxTokensLimit = 20000
	// DefaultGlobalShare is the share of the places and tokens of a prompt
	// that global lessons may use, so they cannot crowd out specific ones.
	DefaultGlobalShare = 0.3
)

// MaxTextChars is the most characters the text of a lesson may have.
const MaxTextChars = 400
