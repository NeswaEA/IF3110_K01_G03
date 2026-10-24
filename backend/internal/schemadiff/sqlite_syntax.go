package schemadiff

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type sqliteToken struct {
	value   string
	quoted  bool
	literal bool
}

func sqliteTokens(sql string) ([]sqliteToken, bool) {
	return sqliteTokensWithComments(sql, false)
}

func sqliteTokensWithComments(sql string, allowComments bool) ([]sqliteToken, bool) {
	if !utf8.ValidString(sql) || strings.ContainsRune(sql, 0) {
		return nil, false
	}
	tokens := []sqliteToken{}
	for i := 0; i < len(sql); {
		if unicode.IsSpace(rune(sql[i])) {
			i++
			continue
		}
		if strings.HasPrefix(sql[i:], "--") {
			if !allowComments {
				return nil, false
			}
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		if strings.HasPrefix(sql[i:], "/*") {
			if !allowComments {
				return nil, false
			}
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return nil, false
			}
			i += end + 4
			continue
		}
		start, quote := i, sql[i]
		if quote == '\'' || quote == '"' || quote == '`' || quote == '[' {
			end := quote
			if quote == '[' {
				end = ']'
			}
			i++
			var value strings.Builder
			closed := false
			for i < len(sql) {
				if sql[i] == end {
					if end != ']' && i+1 < len(sql) && sql[i+1] == end {
						value.WriteByte(end)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				value.WriteByte(sql[i])
				i++
			}
			if !closed {
				return nil, false
			}
			v := value.String()
			if quote == '\'' {
				v = sql[start:i]
			}
			tokens = append(tokens, sqliteToken{value: v, quoted: true, literal: quote == '\''})
			continue
		}
		if sqliteWord(sql[i]) {
			for i < len(sql) && sqliteWord(sql[i]) {
				i++
			}
			tokens = append(tokens, sqliteToken{value: strings.ToUpper(sql[start:i])})
			continue
		}
		tokens = append(tokens, sqliteToken{value: sql[i : i+1]})
		i++
	}
	return tokens, true
}

func sqliteWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}

func sqliteExpression(sql string) bool {
	tokens, ok := sqliteTokens(sql)
	if !ok || len(tokens) == 0 {
		return false
	}
	depth := 0
	for _, t := range tokens {
		if t.quoted {
			continue
		}
		switch t.value {
		case "(":
			depth++
		case ")":
			depth--
			if depth < 0 {
				return false
			}
		case ";", "?", ":", "@", "$":
			return false
		}
	}
	return depth == 0
}

var sqliteNumber = regexp.MustCompile(`^[+-]?(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?|0[xX][0-9a-fA-F]+)$`)
var sqliteBlob = regexp.MustCompile(`(?i)^x'(?:[0-9a-f]{2})*'$`)

func sqliteLiteral(sql string) (bool, bool) {
	sql = strings.TrimSpace(sql)
	if strings.EqualFold(sql, "NULL") {
		return true, true
	}
	if strings.EqualFold(sql, "TRUE") || strings.EqualFold(sql, "FALSE") || sqliteNumber.MatchString(sql) || sqliteBlob.MatchString(sql) {
		return true, false
	}
	tokens, ok := sqliteTokens(sql)
	return ok && len(tokens) == 1 && tokens[0].literal, false
}

func sqliteCheck(sql string) bool {
	tokens, ok := sqliteTokens(sql)
	if !ok || len(tokens) < 4 || tokens[0] != (sqliteToken{value: "CHECK"}) || tokens[1] != (sqliteToken{value: "("}) {
		return false
	}
	depth := 0
	for i, t := range tokens[1:] {
		if t.quoted {
			continue
		}
		if t.value == "(" {
			depth++
		}
		if t.value == ")" {
			depth--
		}
		if depth == 0 && i != len(tokens)-2 {
			return false
		}
	}
	return depth == 0 && sqliteExpression(sql)
}

func sqlitePlainIndex(sql, table, name string, columns []string, unique bool) bool {
	tokens, ok := sqliteTokens(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if !ok {
		return false
	}
	i := 0
	word := func(s string) bool {
		if i >= len(tokens) || tokens[i].quoted || tokens[i].value != s {
			return false
		}
		i++
		return true
	}
	ident := func(s string) bool {
		if i >= len(tokens) || !strings.EqualFold(tokens[i].value, s) {
			return false
		}
		i++
		return true
	}
	if !word("CREATE") {
		return false
	}
	if unique && !word("UNIQUE") {
		return false
	}
	if !word("INDEX") {
		return false
	}
	if i < len(tokens) && tokens[i].value == "IF" && !tokens[i].quoted {
		if !word("IF") || !word("NOT") || !word("EXISTS") {
			return false
		}
	}
	if i+1 < len(tokens) && tokens[i+1].value == "." {
		if !ident("main") || !word(".") {
			return false
		}
	}
	if !ident(name) || !word("ON") || !ident(table) || !word("(") || len(columns) == 0 {
		return false
	}
	for n, column := range columns {
		if n > 0 && !word(",") {
			return false
		}
		if !ident(column) {
			return false
		}
	}
	return word(")") && i == len(tokens)
}
