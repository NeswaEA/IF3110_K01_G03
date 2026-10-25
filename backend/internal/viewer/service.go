// Package viewer serves schema snapshots and table data of a registered target.
//
// Authorization (database.view) is enforced by the HTTP handler/service entry
// once contracts.AccessChecker is available; this layer already never accepts
// SQL, validates paging, hides reserved objects and maps target errors.
package viewer

import (
	"context"
	"errors"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

// TableReader is implemented by database/postgres.Reader and database/sqlite.Reader.
type TableReader interface {
	ReadTable(ctx context.Context, t database.TargetHandle, schema, table string, limit, offset int) (database.TableData, bool, error)
}

var ErrInvalid = errors.New("viewer: invalid request")

// Paging bounds.
const (
	DefaultLimit = 50
	MaxLimit     = 200
	MaxOffset    = 100000
)

type Service struct {
	provider  database.TargetProvider
	inspector database.Inspector
	readers   map[database.Driver]TableReader
	timeout   time.Duration
}

// NewService takes TARGET_QUERY_TIMEOUT as timeout (default 10s).
func NewService(p database.TargetProvider, in database.Inspector, readers map[database.Driver]TableReader, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Service{provider: p, inspector: in, readers: readers, timeout: timeout}
}

func (s *Service) Schema(ctx context.Context, databaseID string) (database.SchemaSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	h, release, err := s.provider.Acquire(ctx, databaseID)
	if err != nil {
		return database.SchemaSnapshot{}, err
	}
	defer release()
	return s.inspector.Inspect(ctx, h)
}

// Data returns one page of a table. limit 0 means the default.
func (s *Service) Data(ctx context.Context, databaseID, schema, table string, limit, offset int) (database.TableData, platform.Page, error) {
	if limit == 0 {
		limit = DefaultLimit
	}
	switch {
	case schema == "" || table == "":
		return database.TableData{}, platform.Page{}, ErrInvalid
	case limit < 1 || limit > MaxLimit, offset < 0 || offset > MaxOffset:
		return database.TableData{}, platform.Page{}, ErrInvalid
	case database.Reserved(schema, table):
		return database.TableData{}, platform.Page{}, database.ErrNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	h, release, err := s.provider.Acquire(ctx, databaseID)
	if err != nil {
		return database.TableData{}, platform.Page{}, err
	}
	defer release()
	r, ok := s.readers[h.Driver]
	if !ok {
		return database.TableData{}, platform.Page{}, database.ErrTargetUnavailable
	}
	data, more, err := r.ReadTable(ctx, h, schema, table, limit, offset)
	if err != nil {
		return database.TableData{}, platform.Page{}, err
	}
	return data, platform.Page{Limit: limit, Offset: offset, HasMore: more}, nil
}
