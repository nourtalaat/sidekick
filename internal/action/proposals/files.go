package proposals

import "github.com/nourtalaat/sidekick/internal/action"

// ReadsFiles reports whether the action with this name is configured and can
// read files from its host, so a person can open the diff of a file of one of
// its proposals. It makes no network call.
func (s *Service) ReadsFiles(name string) bool {
	_, ok := s.opts.Actions[name].(action.FileReader)

	return ok
}
