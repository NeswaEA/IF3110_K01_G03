package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	pgquery "github.com/pganalyze/pg_query_go/v6"
)

type LintFinding struct {
	Rule           string `json:"rule"`
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	Message        string `json:"message"`
	Direction      string `json:"direction"`
	StatementIndex *int   `json:"statement_index"`
}

type lintReport struct {
	findings  []LintFinding
	direction string
	statement *int
}

func (l *lintReport) add(rule, severity, category, message string) {
	l.findings = append(l.findings, LintFinding{Rule: rule, Severity: severity, Category: category,
		Message: message, Direction: l.direction, StatementIndex: l.statement})
}

func (l *lintReport) parseError(engine string) {
	l.add("parse_error", "error", "safety", "Sintaks SQL "+engine+" tidak valid atau tidak memuat statement.")
}

func (l *lintReport) unsupported() {
	l.add("unsupported_statement", "error", "safety", "Operasi SQL ini tidak didukung dalam migrasi transaksional.")
}

func (l *lintReport) unboundParameter() {
	l.add("unbound_parameter", "error", "safety", "Mode normal tidak mengisi parameter SQL.")
}

func (l *lintReport) destructiveDrop() {
	l.add("destructive_drop", "warning", "data_loss", "Operasi ini dapat menghapus data yang tidak bisa dipulihkan oleh SQL down.")
}

func (l *lintReport) unboundedDML() {
	l.add("unbounded_dml", "warning", "data_loss", "UPDATE atau DELETE tanpa WHERE dapat mengubah seluruh tabel.")
}

func (l *lintReport) breakingRename() {
	l.add("breaking_rename", "warning", "compatibility", "Mengganti nama tabel atau kolom dapat memutus kompatibilitas aplikasi.")
}

func (l *lintReport) notNullWithoutDefault() {
	l.add("not_null_without_default", "warning", "compatibility", "NOT NULL tanpa nilai default dapat gagal pada data yang sudah ada.")
}

func (l *lintReport) indexBlocksWrites() {
	l.add("index_blocks_writes", "info", "locking", "Pembuatan indeks dapat memblokir penulisan selama migrasi.")
}

func lintRevision(ctx context.Context, driver database.Driver, r revisionContent) ([]LintFinding, error) {
	report := lintReport{findings: []LintFinding{}}
	var scratch *sql.Conn
	if driver == database.SQLite {
		db, err := sql.Open("sqlite3", ":memory:")
		if err != nil {
			return nil, err
		}
		defer db.Close()
		scratch, err = db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		defer scratch.Close()
	}
	for _, input := range []struct {
		direction string
		sql       *string
	}{{"up", &r.UpSQL}, {"down", r.DownSQL}} {
		if input.sql == nil {
			continue
		}
		report.direction, report.statement = input.direction, nil
		var err error
		if driver == database.Postgres {
			err = report.postgres(*input.sql, r.Batch)
		} else {
			err = report.sqlite(scratch, *input.sql)
		}
		if err != nil {
			return nil, err
		}
	}
	if r.IrreversibleReason != nil {
		report.direction, report.statement = "up", nil
		report.add("irreversible", "info", "reversibility", "Migrasi ini ditandai irreversible.")
	}
	return report.findings, nil
}

func (l *lintReport) postgres(query string, batch *BatchConfig) error {
	tree, err := pgquery.Parse(query)
	if err != nil || len(tree.Stmts) == 0 {
		l.parseError("PostgreSQL")
		return nil
	}
	ast, err := postgresJSONStatements(query)
	if err != nil {
		return err
	}
	if len(ast) != len(tree.Stmts) {
		return fmt.Errorf("PostgreSQL parser returned different statement counts")
	}
	if batch != nil && len(tree.Stmts) != 1 {
		l.batchTemplate()
	}
	for i, raw := range tree.Stmts {
		index := i + 1
		l.statement = &index
		params := make(map[int]bool)
		forbidden := inspectPostgresAST(ast[i].Stmt, params)
		if forbidden || !postgresStatementAllowed(raw.Stmt) {
			l.unsupported()
		}
		if batch == nil {
			if len(params) != 0 {
				l.unboundParameter()
			}
		} else if !validBatchStatement(raw.Stmt, params, *batch) {
			l.batchTemplate()
		}
		l.postgresWarnings(raw.Stmt, batch == nil)
	}
	return nil
}

func (l *lintReport) batchTemplate() {
	l.add("batch_template", "error", "batch", "Gunakan satu UPDATE, DELETE, atau INSERT ... SELECT dengan $1, $2, $3 dan RETURNING kolom key.")
}

func validBatchStatement(statement *pgquery.Node, params map[int]bool, batch BatchConfig) bool {
	if len(params) != 3 || !params[1] || !params[2] || !params[3] {
		return false
	}
	var relation *pgquery.RangeVar
	var returning []*pgquery.Node
	switch n := statement.Node.(type) {
	case *pgquery.Node_UpdateStmt:
		relation, returning = n.UpdateStmt.Relation, n.UpdateStmt.ReturningList
	case *pgquery.Node_DeleteStmt:
		relation, returning = n.DeleteStmt.Relation, n.DeleteStmt.ReturningList
	case *pgquery.Node_InsertStmt:
		selectStmt := n.InsertStmt.GetSelectStmt().GetSelectStmt()
		if selectStmt == nil || len(selectStmt.ValuesLists) != 0 {
			return false
		}
		relation, returning = n.InsertStmt.Relation, n.InsertStmt.ReturningList
	default:
		return false
	}
	schema := relation.GetSchemaname()
	if schema == "" {
		schema = "public"
	}
	if schema != batch.Schema || relation.GetRelname() != batch.Table || len(returning) != 1 {
		return false
	}
	column := returning[0].GetResTarget().GetVal().GetColumnRef()
	return column != nil && len(column.Fields) > 0 &&
		column.Fields[len(column.Fields)-1].GetString_().GetSval() == batch.KeyColumn
}

func (l *lintReport) postgresWarnings(statement *pgquery.Node, normal bool) {
	switch n := statement.Node.(type) {
	case *pgquery.Node_TruncateStmt:
		l.destructiveDrop()
	case *pgquery.Node_DropStmt:
		if n.DropStmt.RemoveType == pgquery.ObjectType_OBJECT_TABLE {
			l.destructiveDrop()
		}
	case *pgquery.Node_UpdateStmt:
		if normal && n.UpdateStmt.WhereClause == nil {
			l.unboundedDML()
		}
	case *pgquery.Node_DeleteStmt:
		if normal && n.DeleteStmt.WhereClause == nil {
			l.unboundedDML()
		}
	case *pgquery.Node_RenameStmt:
		if n.RenameStmt.RenameType == pgquery.ObjectType_OBJECT_TABLE || n.RenameStmt.RenameType == pgquery.ObjectType_OBJECT_COLUMN {
			l.breakingRename()
		}
	case *pgquery.Node_AlterTableStmt:
		for _, cmd := range n.AlterTableStmt.Cmds {
			l.alterTableWarning(cmd.GetAlterTableCmd())
		}
	case *pgquery.Node_IndexStmt:
		if !n.IndexStmt.Concurrent {
			l.indexBlocksWrites()
		}
	}
}

func (l *lintReport) alterTableWarning(cmd *pgquery.AlterTableCmd) {
	switch cmd.GetSubtype() {
	case pgquery.AlterTableType_AT_DropColumn:
		l.destructiveDrop()
	case pgquery.AlterTableType_AT_AlterColumnType:
		l.add("breaking_type_change", "warning", "compatibility", "Mengubah tipe kolom dapat memutus kompatibilitas aplikasi.")
		l.exclusiveLock()
	case pgquery.AlterTableType_AT_SetNotNull:
		l.notNullWithoutDefault()
		l.exclusiveLock()
	case pgquery.AlterTableType_AT_AddConstraint:
		if !cmd.GetDef().GetConstraint().GetSkipValidation() {
			l.exclusiveLock()
		}
	case pgquery.AlterTableType_AT_AddColumn:
		column := cmd.GetDef().GetColumnDef()
		hasDefault, notNull := column.GetRawDefault() != nil, column.GetIsNotNull()
		for _, constraint := range column.GetConstraints() {
			switch constraint.GetConstraint().GetContype() {
			case pgquery.ConstrType_CONSTR_NOTNULL:
				notNull = true
			case pgquery.ConstrType_CONSTR_DEFAULT, pgquery.ConstrType_CONSTR_IDENTITY, pgquery.ConstrType_CONSTR_GENERATED:
				hasDefault = true
			}
		}
		if notNull && !hasDefault {
			l.notNullWithoutDefault()
		}
	}
}

func (l *lintReport) exclusiveLock() {
	l.add("exclusive_lock", "warning", "locking", "Perubahan ini dapat mengambil lock eksklusif pada tabel.")
}
