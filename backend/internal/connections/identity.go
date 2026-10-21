package connections

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// IdentityFromDSN returns the target identity of a PostgreSQL DSN (URL or
// keyword form), in the same format as Config.TargetIdentity. The composition
// root passes METADATA_DATABASE_URL here so the metadata database can never be
// registered as a target. The DSN is parsed locally and never logged.
func IdentityFromDSN(dsn string) (string, error) {
	c, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return "", fmt.Errorf("connections: cannot parse metadata DSN") // no DSN in the message
	}
	return fmt.Sprintf("postgres:%s:%d/%s", strings.ToLower(c.Host), c.Port, c.Database), nil
}
