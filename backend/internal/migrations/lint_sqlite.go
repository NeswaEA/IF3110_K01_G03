package migrations

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type sqliteToken struct {
	kind       byte
	value      string
	start, end int
}

func sqliteWordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 128
}

func sqliteTokens(query string) []sqliteToken {
	var tokens []sqliteToken
	for i := 0; i < len(query); {
		start := i
		switch {
		case strings.HasPrefix(query[i:], "--"):
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				return tokens
			}
			i += end + 1
		case strings.HasPrefix(query[i:], "/*"):
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				return tokens
			}
			i += end + 4
		case strings.ContainsRune(" \t\n\r\f\v", rune(query[i])):
			i++
		case strings.ContainsRune("'\x22`[", rune(query[i])):
			quote, closing := query[i], query[i]
			if quote == '[' {
				closing = ']'
			}
			i++
			var value strings.Builder
			for i < len(query) {
				char := query[i]
				i++
				if char == closing {
					if quote == '[' || i == len(query) || query[i] != closing {
						break
					}
					i++
				}
				value.WriteByte(char)
			}
			tokens = append(tokens, sqliteToken{quote, value.String(), start, i})
		case sqliteWordByte(query[i]):
			i++
			for i < len(query) && (sqliteWordByte(query[i]) || query[i] == '$') {
				i++
			}
			tokens = append(tokens, sqliteToken{'w', query[start:i], start, i})
		default:
			i++
			tokens = append(tokens, sqliteToken{query[start], query[start:i], start, i})
		}
	}
	return tokens
}

func sqliteReservedName(query string) bool {
	return sqliteReservedTokens(sqliteTokens(query))
}

func sqliteReservedTokens(tokens []sqliteToken) bool {
	for i, token := range tokens {
		if !strings.HasPrefix(strings.ToLower(token.value), "_platform_") {
			continue
		}
		if token.kind != '\'' || i >= 2 && strings.EqualFold(tokens[i-2].value, "rename") && strings.EqualFold(tokens[i-1].value, "to") {
			return true
		}
	}
	return false
}

type sqliteStatement struct {
	query  string
	tokens []sqliteToken
}

func sqliteStatements(query string) []sqliteStatement {
	tokens := sqliteTokens(query)
	var statements []sqliteStatement
	for start := 0; start < len(tokens); {
		end := start
		for end < len(tokens) && tokens[end].kind != ';' {
			end++
		}
		if end > start {
			statements = append(statements, sqliteStatement{
				query: query[tokens[start].start:tokens[end-1].end], tokens: tokens[start:end],
			})
		}
		start = end + 1
	}
	return statements
}

func (l *lintReport) sqlite(conn *sql.Conn, query string) error {
	statements := sqliteStatements(query)
	if len(statements) == 0 {
		l.parseError("SQLite")
	}
	for i, statement := range statements {
		l.statement = new(i + 1)
		if err := l.sqliteStatement(conn, statement.query, statement.tokens); err != nil {
			return err
		}
	}
	return nil
}

func (l *lintReport) sqliteStatement(conn *sql.Conn, query string, tokens []sqliteToken) error {
	words := sqliteTopLevelWords(tokens)
	first := ""
	if tokens[0].kind == 'w' {
		first = strings.ToUpper(tokens[0].value)
	}
	if sqliteReservedTokens(tokens) || first == "EXPLAIN" || first == "VACUUM" {
		l.unsupported()
		return nil
	}
	denied := false
	err := conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected SQLite driver %T", driverConn)
		}
		c.RegisterAuthorizer(func(op int, a1, a2, a3 string) int {
			result := migrationAuthorizer(op, a1, a2, a3)
			denied = denied || result == sqlite3.SQLITE_DENY
			return result
		})
		defer c.RegisterAuthorizer(nil)
		statement, err := c.Prepare(query)
		if err != nil {
			return err
		}
		return statement.Close()
	})
	switch {
	case denied:
		l.unsupported()
	case err != nil && !sqliteNeedsSchema(err):
		var engineError sqlite3.Error
		if !errors.As(err, &engineError) {
			return err
		}
		l.parseError("SQLite")
	}
	for _, token := range tokens {
		if token.kind == '?' || token.kind == ':' || token.kind == '@' || token.kind == '$' {
			l.unboundParameter()
			break
		}
	}
	l.sqliteWarnings(words)
	return nil
}

// Preparing against an empty catalog can resolve syntax and safety without
// running user SQL. The worker resolves schema-dependent names on the target.
func sqliteNeedsSchema(err error) bool {
	var engineError sqlite3.Error
	if !errors.As(err, &engineError) || engineError.Code != sqlite3.ErrError {
		return false
	}
	message := engineError.Error()
	return strings.HasPrefix(message, "no such table:") || strings.HasPrefix(message, "no such column:") ||
		strings.HasPrefix(message, "no such function:") || strings.HasPrefix(message, "no such index:") ||
		strings.HasPrefix(message, "ambiguous column name:") || message == "unable to identify the object to be reindexed"
}

func sqliteTopLevelWords(tokens []sqliteToken) []string {
	var words []string
	depth := 0
	for _, token := range tokens {
		switch token.kind {
		case '(':
			depth++
		case ')':
			depth--
		case 'w':
			if depth == 0 {
				words = append(words, strings.ToUpper(token.value))
			}
		}
	}
	return words
}

func (l *lintReport) sqliteWarnings(words []string) {
	if len(words) == 0 {
		return
	}
	command := words[0]
	if command == "WITH" {
		for _, word := range words[1:] {
			if word == "UPDATE" || word == "DELETE" || word == "SELECT" || word == "INSERT" {
				command = word
				break
			}
		}
	}
	if (command == "UPDATE" || command == "DELETE") && !slices.Contains(words, "WHERE") {
		l.unboundedDML()
	}
	if command == "DROP" && len(words) > 1 && words[1] == "TABLE" {
		l.destructiveDrop()
	}
	if command == "ALTER" && slices.Contains(words, "RENAME") {
		l.breakingRename()
	}
	if command == "ALTER" && slices.Contains(words, "DROP") {
		l.destructiveDrop()
	}
	if command == "ALTER" && slices.Contains(words, "ADD") && !slices.Contains(words, "DEFAULT") {
		for i := 1; i < len(words); i++ {
			if words[i-1] == "NOT" && words[i] == "NULL" {
				l.notNullWithoutDefault()
				break
			}
		}
	}
	if command == "CREATE" && len(words) > 1 &&
		(words[1] == "INDEX" || words[1] == "UNIQUE" && len(words) > 2 && words[2] == "INDEX") {
		l.indexBlocksWrites()
	}
}
