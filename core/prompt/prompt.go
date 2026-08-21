// Package prompt renders the {placeholder} templates used by runtime prompts.
// Missing variables are errors rather than silently empty substitutions,
// because losing an identity or safety value is a correctness bug.
//
// The renderer keeps the compact {name} syntax used by the built-in prompts
// and avoids introducing a template dependency for this small feature.
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
