package failure

import (
	"regexp"
	"strings"

	"github.com/gobwas/glob"
)

// secretPatterns catch credentials CI logs commonly leak. They err on the
// side of redacting too much: the output is for humans, not machines.
var secretPatterns = []*regexp.Regexp{
	// key=value / key: value for secret-looking keys.
	regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:token|secret|password|passwd|api[_-]?key|access[_-]?key|private[_-]?key|credential)[a-z0-9_.-]*)(\s*[:=]\s*)("[^"]*"|'[^']*'|\S+)`),
	// HTTP authorization headers.
	regexp.MustCompile(`(?i)\b(authorization)(\s*:\s*)(\S+\s+\S+|\S+)`),
	regexp.MustCompile(`(?i)\b(bearer)(\s+)([A-Za-z0-9._~+/=-]{8,})`),
	// Credentials embedded in URLs.
	regexp.MustCompile(`(://)([^/\s:@]+:)([^@\s]+)@`),
	// Well-known token formats (GitHub, GitLab, Slack, AWS, JWTs).
	regexp.MustCompile(`()()\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[abpr]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,})`),
	// PEM private keys.
	regexp.MustCompile(`()()(-----BEGIN [A-Z ]*PRIVATE KEY-----)`),
}

const redacted = "[REDACTED]"

type redactor struct{ literals []string }

func newRedactor(secrets []string) *redactor {
	r := &redactor{}
	for _, s := range secrets {
		// Very short values would redact ordinary words.
		if len(s) >= 6 {
			r.literals = append(r.literals, s)
		}
	}
	return r
}

func (r *redactor) apply(s string) string {
	for _, l := range r.literals {
		s = strings.ReplaceAll(s, l, redacted)
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "${1}${2}"+redacted)
	}
	return s
}

func (r *redactor) lines(ls []string) []string {
	for i := range ls {
		ls[i] = r.apply(ls[i])
	}
	return ls
}

// pathMatch matches a required-check pattern the same way the monitor does.
func pathMatch(pattern, name string) (bool, error) {
	g, err := glob.Compile(pattern)
	if err != nil {
		return false, err
	}
	return g.Match(name), nil
}
