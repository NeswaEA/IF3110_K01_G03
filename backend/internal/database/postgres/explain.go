package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Planner runs EXPLAIN (FORMAT JSON) without ANALYZE inside a READ ONLY
// transaction that is always rolled back. The caller (explain package) has
// already checked that sql is exactly one plannable statement.
type Planner struct{}

func (Planner) Explain(ctx context.Context, t database.TargetHandle, stmt string) (database.PlanResult, error) {
	tx, err := t.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return database.PlanResult{}, database.ClassifyTargetError(ctx, err)
	}
	defer tx.Rollback()

	var out string
	if err := tx.QueryRowContext(ctx, "EXPLAIN (FORMAT JSON) "+stmt).Scan(&out); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return database.PlanResult{}, &database.EngineError{Message: pgErr.Message}
		}
		return database.PlanResult{}, database.ClassifyTargetError(ctx, err)
	}
	return parsePlan(out)
}

type pgNode struct {
	NodeType     string   `json:"Node Type"`
	RelationName *string  `json:"Relation Name"`
	IndexName    *string  `json:"Index Name"`
	PlanRows     *float64 `json:"Plan Rows"`
	TotalCost    *float64 `json:"Total Cost"`
	Plans        []pgNode `json:"Plans"`
}

func parsePlan(out string) (database.PlanResult, error) {
	var raw any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return database.PlanResult{}, err
	}
	var doc []struct {
		Plan pgNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc) == 0 {
		return database.PlanResult{}, errors.New("postgres: unexpected EXPLAIN output")
	}
	res := database.PlanResult{Driver: database.Postgres, Mode: "estimate", Nodes: []database.PlanNode{}, Raw: raw, Warnings: []string{}}
	var walk func(n pgNode, parent *string)
	walk = func(n pgNode, parent *string) {
		id := strconv.Itoa(len(res.Nodes))
		res.Nodes = append(res.Nodes, database.PlanNode{ID: id, ParentID: parent, Operation: n.NodeType,
			Relation: n.RelationName, Index: n.IndexName, EstimatedRows: n.PlanRows, EstimatedCost: n.TotalCost})
		for _, c := range n.Plans {
			walk(c, &id)
		}
	}
	walk(doc[0].Plan, nil)
	return res, nil
}
