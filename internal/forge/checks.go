package forge

import (
	"context"
	"regexp"
)

// prOnlyContext matches status contexts that Gitea/Forgejo Actions name after
// a pull request event: "<workflow> / <job> (pull_request)". Merge and batch
// branches are pushed branches, so such a context never reports there; on
// the merge branch the same job shows up as "... (push)".
var prOnlyContext = regexp.MustCompile(`\(pull_request[a-z_]*\)$`)

// PullRequestOnlyContexts returns the checks that can only report for pull
// request events and therefore never on a merge branch.
func PullRequestOnlyContexts(checks []string) []string {
	var out []string
	for _, c := range checks {
		if prOnlyContext.MatchString(c) {
			out = append(out, c)
		}
	}
	return out
}

// ProtectionLister is optionally implemented by forges that can list the
// required checks of every branch protection rule in one call (Gitea). It is
// used for startup diagnostics only.
type ProtectionLister interface {
	// ListProtectedChecks maps rule name to its required checks, excluding
	// gitea-mq's own context. Rules without status checks are omitted.
	ListProtectedChecks(ctx context.Context, owner, name string) (map[string][]string, error)
}
