package contracts

import "regexp"

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidID reports whether s is a lowercase UUID, the only id form the API and pages accept.
func ValidID(s string) bool { return idPattern.MatchString(s) }
