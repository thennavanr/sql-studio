package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DbObject is a schema-scoped object shown in the object explorer.
type DbObject struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // table, view, procedure, function
}

// ColumnInfo describes a table/view column.
type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // e.g. nvarchar(50), decimal(18,2)
	Nullable bool   `json:"nullable"`
	PK       bool   `json:"pk"`
	Identity bool   `json:"identity"`
	Computed bool   `json:"computed"`
}

// Databases lists databases the login can access.
func Databases(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name FROM sys.databases
		WHERE HAS_DBACCESS(name) = 1 AND state = 0
		ORDER BY CASE WHEN database_id <= 4 THEN 1 ELSE 0 END, name`)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

// Objects lists tables, views, procedures and functions in the pool's database.
func Objects(ctx context.Context, db *sql.DB) ([]DbObject, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.name, o.name,
			CASE o.type
				WHEN 'U' THEN 'table' WHEN 'V' THEN 'view' WHEN 'P' THEN 'procedure'
				ELSE 'function' END
		FROM sys.objects o
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE o.type IN ('U','V','P','FN','IF','TF') AND o.is_ms_shipped = 0
		ORDER BY s.name, o.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DbObject{}
	for rows.Next() {
		var o DbObject
		if err := rows.Scan(&o.Schema, &o.Name, &o.Kind); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Columns lists the columns of a table or view.
func Columns(ctx context.Context, db *sql.DB, schema, name string) ([]ColumnInfo, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.name, t.name, c.max_length, c.precision, c.scale, c.is_nullable,
			CAST(CASE WHEN EXISTS (
				SELECT 1 FROM sys.index_columns ic
				JOIN sys.indexes i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
				WHERE i.is_primary_key = 1 AND ic.object_id = c.object_id AND ic.column_id = c.column_id
			) THEN 1 ELSE 0 END AS bit),
			c.is_identity, c.is_computed
		FROM sys.columns c
		JOIN sys.types t ON t.user_type_id = c.user_type_id
		WHERE c.object_id = OBJECT_ID(@p1)
		ORDER BY c.column_id`, QuoteName(schema)+"."+QuoteName(name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ColumnInfo{}
	for rows.Next() {
		var (
			c                ColumnInfo
			typ              string
			maxLen           int16
			precision, scale uint8
		)
		if err := rows.Scan(&c.Name, &typ, &maxLen, &precision, &scale, &c.Nullable, &c.PK, &c.Identity, &c.Computed); err != nil {
			return nil, err
		}
		c.Type = FormatType(typ, int(maxLen), int(precision), int(scale))
		out = append(out, c)
	}
	return out, rows.Err()
}

// FormatType renders a SQL type with its length/precision, e.g. nvarchar(50).
func FormatType(typ string, maxLen, precision, scale int) string {
	switch strings.ToLower(typ) {
	case "varchar", "char", "varbinary", "binary":
		if maxLen == -1 {
			return typ + "(max)"
		}
		return fmt.Sprintf("%s(%d)", typ, maxLen)
	case "nvarchar", "nchar":
		if maxLen == -1 {
			return typ + "(max)"
		}
		return fmt.Sprintf("%s(%d)", typ, maxLen/2)
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", typ, precision, scale)
	case "datetime2", "time", "datetimeoffset":
		return fmt.Sprintf("%s(%d)", typ, scale)
	}
	return typ
}

// CompletionSchema returns schema.table -> columns for editor autocompletion.
func CompletionSchema(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT TOP (50000) s.name, o.name, c.name
		FROM sys.objects o
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		JOIN sys.columns c ON c.object_id = o.object_id
		WHERE o.type IN ('U','V') AND o.is_ms_shipped = 0
		ORDER BY s.name, o.name, c.column_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var s, t, c string
		if err := rows.Scan(&s, &t, &c); err != nil {
			return nil, err
		}
		key := s + "." + t
		out[key] = append(out[key], c)
	}
	return out, rows.Err()
}

// Definition returns the CREATE script for a view/procedure/function, or a
// generated CREATE TABLE for tables.
func Definition(ctx context.Context, db *sql.DB, schema, name, kind string) (string, error) {
	full := QuoteName(schema) + "." + QuoteName(name)
	if kind != "table" {
		var def sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))", full).Scan(&def); err != nil {
			return "", err
		}
		if !def.Valid {
			return "", fmt.Errorf("definition of %s is not available (encrypted or no permission)", full)
		}
		return strings.TrimSpace(def.String) + "\nGO\n", nil
	}
	cols, err := Columns(ctx, db, schema, name)
	if err != nil {
		return "", err
	}
	return createTableScript(cols, full, name), nil
}

// createTableScript renders CREATE TABLE <target> for the given columns.
// pkBase names the primary key constraint (PK_<pkBase>).
func createTableScript(cols []ColumnInfo, target, pkBase string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "CREATE TABLE %s (\n", target)
	var pk []string
	hasPK := false
	for _, c := range cols {
		hasPK = hasPK || c.PK
	}
	for i, c := range cols {
		fmt.Fprintf(&sb, "    %s %s", QuoteName(c.Name), c.Type)
		if c.Identity {
			sb.WriteString(" IDENTITY(1,1)")
		}
		if c.Nullable {
			sb.WriteString(" NULL")
		} else {
			sb.WriteString(" NOT NULL")
		}
		if c.PK {
			pk = append(pk, QuoteName(c.Name))
		}
		if i < len(cols)-1 || hasPK {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	if len(pk) > 0 {
		fmt.Fprintf(&sb, "    CONSTRAINT %s PRIMARY KEY (%s)\n", QuoteName("PK_"+pkBase), strings.Join(pk, ", "))
	}
	sb.WriteString(");\nGO\n")
	return sb.String()
}

// QuoteName brackets an identifier like T-SQL QUOTENAME.
func QuoteName(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
