package auth

import (
	"net/url"
	"strings"
	"unicode"
)

func SafeNext(value string) string {
	u, err := url.ParseRequestURI(value)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, "\\") || strings.Contains(u.Path, "%") || strings.ContainsFunc(u.Path, unicode.IsControl) {
		return "/databases"
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "/databases"
		}
	}
	return u.RequestURI()
}
