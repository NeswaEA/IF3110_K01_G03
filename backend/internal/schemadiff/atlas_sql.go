package schemadiff

import (
	"encoding/json"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	pgquery "github.com/pganalyze/pg_query_go/v6"
)

func atlasTransactionalDDL(sql, schema string) bool {
	tree, err := pgquery.Parse(sql)
	if err != nil || len(tree.Stmts) == 0 {
		return false
	}
	relation := func(r *pgquery.RangeVar) bool {
		return r != nil && r.Catalogname == "" && (r.Schemaname == schema || r.Schemaname == "" && schema == "public") && r.Relpersistence != "t" && !database.Reserved(schema, r.Relname)
	}
	for _, raw := range tree.Stmts {
		switch n := raw.Stmt.Node.(type) {
		case *pgquery.Node_CreateStmt:
			s := n.CreateStmt
			if !relation(s.Relation) || len(s.InhRelations) > 0 || s.Partbound != nil || s.Partspec != nil || s.OfTypename != nil || s.Tablespacename != "" {
				return false
			}
		case *pgquery.Node_IndexStmt:
			if n.IndexStmt.Concurrent || !relation(n.IndexStmt.Relation) || n.IndexStmt.TableSpace != "" {
				return false
			}
		case *pgquery.Node_AlterTableStmt:
			if !relation(n.AlterTableStmt.Relation) {
				return false
			}
			for _, cmd := range n.AlterTableStmt.Cmds {
				if cmd.GetAlterTableCmd() == nil {
					return false
				}
				switch cmd.GetAlterTableCmd().Subtype.String() {
				case "AT_AddColumn", "AT_DropColumn", "AT_AlterColumnType", "AT_ColumnDefault", "AT_DropNotNull", "AT_SetNotNull", "AT_AddConstraint", "AT_DropConstraint", "AT_ValidateConstraint":
				default:
					return false
				}
			}
		case *pgquery.Node_DropStmt:
			if n.DropStmt.Concurrent || n.DropStmt.RemoveType != pgquery.ObjectType_OBJECT_TABLE && n.DropStmt.RemoveType != pgquery.ObjectType_OBJECT_INDEX {
				return false
			}
			for _, object := range n.DropStmt.Objects {
				list := object.GetList()
				if list == nil || len(list.Items) < 1 || len(list.Items) > 2 {
					return false
				}
				name := list.Items[len(list.Items)-1].GetString_().GetSval()
				objectSchema := "public"
				if len(list.Items) == 2 {
					objectSchema = list.Items[0].GetString_().GetSval()
				}
				if name == "" || objectSchema != schema || database.Reserved(objectSchema, name) {
					return false
				}
			}
		case *pgquery.Node_RenameStmt:
			if !relation(n.RenameStmt.Relation) {
				return false
			}
			switch n.RenameStmt.RenameType {
			case pgquery.ObjectType_OBJECT_TABLE, pgquery.ObjectType_OBJECT_COLUMN, pgquery.ObjectType_OBJECT_INDEX, pgquery.ObjectType_OBJECT_TABCONSTRAINT:
			default:
				return false
			}
		default:
			return false
		}
	}
	encoded, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return false
	}
	var ast any
	if json.Unmarshal([]byte(encoded), &ast) != nil {
		return false
	}
	return !atlasUnsafeAST(ast)
}

func atlasUnsafeAST(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if schema, ok := v["schemaname"].(string); ok && (schema == "_platform" || schema == "information_schema" || strings.HasPrefix(schema, "pg_")) {
			return true
		}
		if catalog, ok := v["catalogname"].(string); ok && catalog != "" {
			return true
		}
		if _, ok := v["ParamRef"]; ok {
			return true
		}
		if call, ok := v["FuncCall"].(map[string]any); ok {
			if names, ok := call["funcname"].([]any); ok && len(names) > 0 {
				part, _ := names[len(names)-1].(map[string]any)
				name, _ := part["String"].(map[string]any)
				last, _ := name["sval"].(string)
				last = strings.ToLower(last)
				if last == "set_config" || last == "lo_import" || last == "lo_export" || strings.HasPrefix(last, "dblink") || strings.HasPrefix(last, "pg_") {
					return true
				}
			}
		}
		for _, child := range v {
			if atlasUnsafeAST(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if atlasUnsafeAST(child) {
				return true
			}
		}
	}
	return false
}
