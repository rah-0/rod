package proto

import (
	"regexp"
	"strings"
)

// PatternToReg translates a FetchRequestPattern.URLPattern glob to a regular
// expression. Only unescaped '*' and '?' are wildcards; other characters match
// literally. An empty pattern matches every URL, as in FetchRequestPattern.
func PatternToReg(pattern string) string {
	if pattern == "" {
		return ""
	}

	var result strings.Builder
	result.WriteString(`\A`)
	escaped := false
	for _, char := range pattern {
		if escaped {
			result.WriteString(regexp.QuoteMeta(string(char)))
			escaped = false
			continue
		}
		switch char {
		case '\\':
			escaped = true
		case '*':
			result.WriteString(".*")
		case '?':
			result.WriteByte('.')
		default:
			result.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	result.WriteString(`\z`)
	return result.String()
}
