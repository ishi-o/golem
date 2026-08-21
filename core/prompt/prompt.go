// Package prompt renders the {placeholder} templates the runtime's prompts
// use. spring-agent used Spring AI's SystemPromptTemplate, whose one behavior
// worth porting is strictness: a template naming a variable the caller did
// not supply is an error, not a silently empty substitution — a prompt that
// quietly renders "Sender user ID: " is worse than a failed render, because
// nothing downstream notices.
//
// text/template would do the job, but its {{.var}} syntax would break every
// prompt written for spring-agent's {var} syntax, including the two defaults
// this repository ships; a thirty-line strict renderer is cheaper than that
// break.
package prompt

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// Render substitutes each {name} in the template with vars[name].
//
// Every placeholder must have a value: an unknown name fails the render, and
// a name whose value contains a brace is rejected too, since the result would
// then contain what looks like an unsubstituted placeholder and the strictness
// above would be theater.
func Render(template string, vars map[string]string) (string, error) {
	// Validate everything before substituting anything, so a bad template or
	// value fails the render outright instead of producing a half-rendered
	// prompt the caller cannot tell apart from a good one.
	for _, m := range placeholder.FindAllStringSubmatch(template, -1) {
		name := m[1]
		value, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("prompt variable %q is not supplied", name)
		}
		if strings.ContainsAny(value, "{}") {
			return "", fmt.Errorf("value of prompt variable %q contains a brace: %q", name, value)
		}
	}
	return placeholder.ReplaceAllStringFunc(template, func(match string) string {
		return vars[strings.Trim(match, "{}")]
	}), nil
}

// Variables lists the placeholder names a template references, for callers
// that want to validate a configured prompt at startup rather than on the
// first render.
func Variables(template string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(template, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}
