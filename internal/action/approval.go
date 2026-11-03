package action

// ReadWriteGate is embedded, inline, in the config struct of an approval-only
// action, and in the action itself so the built instance reports it too. An
// action that embeds it is approval-only: a route in read-write mode may list
// it only when AllowReadWrite is true. Mutes and the other actions do not
// embed it, so for them allowReadWrite is an unknown key.
type ReadWriteGate struct {
	AllowReadWrite bool `yaml:"allowReadWrite"`
}

// ReadWriteAllowed reports whether the operator set allowReadWrite: true.
func (g ReadWriteGate) ReadWriteAllowed() bool { return g.AllowReadWrite }

// ApprovalOnly is satisfied by any value that embeds ReadWriteGate: a decoded
// action config (a pointer to the plugin's Config) or a built action instance.
type ApprovalOnly interface {
	ReadWriteAllowed() bool
}

// ReadWriteBlocked reports whether v is approval-only and has not opted in,
// so a read-write route must not run it without a person. It is false for
// a value that is not approval-only.
func ReadWriteBlocked(v any) bool {
	gate, ok := v.(ApprovalOnly)

	return ok && !gate.ReadWriteAllowed()
}
