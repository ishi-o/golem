package share

import (
	"path"
	"strings"
)

func safePathComponent(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, `/\\`) && path.Clean(value) == value
}

func safeRelativePath(value string) (string, bool) {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) {
		return "", false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", false
		}
	}
	clean := path.Clean(value)
	if clean == "." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}
