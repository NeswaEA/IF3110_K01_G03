package sqlite

import (
	"strings"
	"unicode"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// parseChecks extracts CHECK constraints from a CREATE TABLE statement, since
// SQLite has no PRAGMA for them. It skips string literals, quoted identifiers
// and comments, and returns the exact CHECK (...) text as the definition.
// A preceding "CONSTRAINT name" becomes the name; otherwise name is null.
// Columns are left empty: SQLite does not record which columns a check uses.
func parseChecks(createSQL string) []database.Constraint {
	var out []database.Constraint
	s := createSQL
	n := len(s)
	var prevWords []string // words since the last ',' or parenthesis

	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := skipQuoted(s, i, c)
			if c != '\'' {
				prevWords = append(prevWords, unquote(s[i:j]))
			}
			i = j
		case c == '[':
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return out
			}
			prevWords = append(prevWords, s[i+1:i+j])
			i += j + 1
		case c == '-' && i+1 < n && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return out
			}
			i += j + 1
		case c == '/' && i+1 < n && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return out
			}
			i += j + 4
		case isWordByte(c):
			j := i
			for j < n && isWordByte(s[j]) {
				j++
			}
			word := s[i:j]
			if strings.EqualFold(word, "CHECK") {
				k := skipGap(s, j)
				if k < n && s[k] == '(' {
					end := matchParen(s, k)
					if end < 0 {
						return out
					}
					def := "CHECK " + s[k:end+1]
					var name *string
					if len(prevWords) >= 2 && strings.EqualFold(prevWords[len(prevWords)-2], "CONSTRAINT") {
						nm := prevWords[len(prevWords)-1]
						name = &nm
					}
					out = append(out, database.Constraint{Name: name, Kind: database.Check, Columns: []string{}, Definition: def})
					prevWords = prevWords[:0]
					i = end + 1
					continue
				}
			}
			prevWords = append(prevWords, word)
			i = j
		default:
			if c == ',' || c == '(' || c == ')' {
				prevWords = prevWords[:0]
			}
			i++
		}
	}
	return out
}

// skipGap returns the index of the next token at or after i, past whitespace
// and comments, which SQLite allows between any two tokens.
func skipGap(s string, i int) int {
	for i < len(s) {
		switch {
		case unicode.IsSpace(rune(s[i])):
			i++
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return len(s)
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return len(s)
			}
			i += j + 4
		default:
			return i
		}
	}
	return i
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// skipQuoted returns the index just past a quoted run starting at i; a doubled
// quote character is an escaped quote.
func skipQuoted(s string, i int, q byte) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

func unquote(tok string) string {
	if len(tok) < 2 {
		return tok
	}
	q := tok[:1]
	return strings.ReplaceAll(tok[1:len(tok)-1], q+q, q)
}

// matchParen returns the index of the ')' matching the '(' at open, honouring
// quotes and comments inside, or -1.
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(s, i, c)
			continue
		case c == '[':
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return -1
			}
			i += j + 1
			continue
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return -1
			}
			i += j + 1
			continue
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return -1
			}
			i += j + 4
			continue
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}
