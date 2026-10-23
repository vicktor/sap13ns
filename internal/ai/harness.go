package ai

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// The revision harness. A task helper's first answer is checked by code
// (lengths, the cliché detector, the Markdown structure, the reply's shape);
// when the checks find problems, the model is shown its answer and the
// problems and asked once for a corrected one. The better of the two
// answers is kept, so a revision never makes the result worse, and a failed
// revision request still returns the first answer.

// checkFunc returns what is wrong with an answer, as instructions the model
// can follow; none means the answer is fine.
type checkFunc func(text string) []string

// maxRevisions bounds the extra requests a task makes.
const maxRevisions = 1

// maxListed bounds the problems shown to the model in one revision.
const maxListed = 10

// askRevised sends req and, when check finds problems, asks for revisions.
// It returns the answer with the fewest problems and those problems.
func askRevised(ctx context.Context, p Provider, req TextRequest, check checkFunc) (*TextResponse, []string, error) {
	res, err := ask(ctx, p, req)
	if err != nil {
		return nil, nil, err
	}
	best, left := res, check(res.Text)
	conv := slices.Clone(req.Messages)
	last := res
	for range maxRevisions {
		if len(left) == 0 || ctx.Err() != nil {
			break
		}
		problems := check(last.Text)
		if len(problems) > maxListed {
			problems = problems[:maxListed]
		}
		conv = append(conv, Message{Role: RoleAssistant, Content: last.Text},
			Message{Role: RoleUser, Content: "Your answer has these problems:\n- " + strings.Join(problems, "\n- ") +
				"\nFix them and send the whole corrected answer again, in the same format, with nothing else."})
		r := req
		r.Messages = conv
		next, err := ask(ctx, p, r)
		if err != nil {
			break // keep the first answer
		}
		if found := check(next.Text); len(found) <= len(left) {
			best, left = next, found
		}
		last = next
	}
	return best, left, nil
}

// lintProblems lists the clichés Lint finds in text, one problem each.
func lintProblems(text, lang string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range Lint(text, lang) {
		phrase := strings.Join(strings.Fields(f.Phrase), " ")
		if seen[f.Rule+phrase] {
			continue
		}
		seen[f.Rule+phrase] = true
		switch f.Rule {
		case RuleEmDash, RuleBold:
			out = append(out, f.Suggestion)
		default:
			out = append(out, fmt.Sprintf("«%s»: %s", limitRunes(phrase, 80), f.Suggestion))
		}
	}
	return out
}

func lengthProblem(what, s string, limit int) []string {
	if n := utf8.RuneCountInString(s); n > limit {
		return []string{fmt.Sprintf("%s has %d characters; the limit is %d. Shorten it, do not cut it mid-sentence.", what, n, limit)}
	}
	return nil
}

// Openers and closers chat models add around the text they were asked
// for. They are narrow on purpose: an article may well end by inviting its
// readers to write, so only lines addressed to the person asking match.
var (
	preambleRe = regexp.MustCompile(`(?i)^\s*(?:(?:¡?claro|¡?por supuesto|sure|certainly|of course)[!,.][^\n]{0,120}|(?:aquí (?:tienes|está|va)|here(?:'s| is| are))[^\n]{0,120}:)\s*\n`)
	closingRe  = regexp.MustCompile(`(?i)\n\s*(?:espero que (?:te|le|os) (?:sirva|ayude)|¿(?:quieres|te gustaría|deseas) que (?:lo|la|los|las|te|le|haga|añada|amplíe|cambie)|si quieres,? (?:puedo|te)|i hope this helps|would you like me to|if you(?:'d)? like,? i can)[^\n]*\s*$`)
)

func wrapperProblems(text string) []string {
	var out []string
	if preambleRe.MatchString(text) {
		out = append(out, "It starts with a preamble addressed to me: start directly with the text itself.")
	}
	if closingRe.MatchString(text) {
		out = append(out, "It ends with a remark or offer addressed to me: end with the text itself.")
	}
	return out
}

// stripWrapper removes what wrapperProblems finds, for an answer that kept
// it after the revision.
func stripWrapper(text string) string {
	text = preambleRe.ReplaceAllString(text, "")
	return strings.TrimSpace(closingRe.ReplaceAllString(text, ""))
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
