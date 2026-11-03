package engine

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mssql "github.com/microsoft/go-mssqldb"
)

// ScriptDataOptions control "Script Data as INSERT".
type ScriptDataOptions struct {
	Top             int    `json:"top"`             // 0 = all rows (up to MaxScriptRows)
	Where           string `json:"where"`           // optional filter, without the WHERE keyword
	OrderBy         string `json:"orderBy"`         // optional, without ORDER BY; defaults to the primary key
	Target          string `json:"target"`          // target table; defaults to the source table
	RowsPerInsert   int    `json:"rowsPerInsert"`   // 1..1000 rows per INSERT statement
	IncludeIdentity bool   `json:"includeIdentity"` // script identity values with SET IDENTITY_INSERT
	IncludeCreate   bool   `json:"includeCreate"`   // prepend CREATE TABLE for the target
}

// ScriptDataResult is the generated script.
type ScriptDataResult struct {
	Script    string `json:"script"`
	Rows      int    `json:"rows"`
	Truncated bool   `json:"truncated"`
	Target    string `json:"target"`
}

// MaxScriptRows bounds a single data script so the editor stays usable.
const MaxScriptRows = 200000

type dataColumn struct {
	name     string
	baseType string // system base type, lower case (e.g. nvarchar, datetime2)
	udt      bool   // CLR type: geography, geometry, hierarchyid
	udtName  string
	identity bool
}

// ScriptData generates INSERT statements containing the rows of a table or view.
func ScriptData(ctx context.Context, db *sql.DB, schema, name string, o ScriptDataOptions) (ScriptDataResult, error) {
	source := QuoteName(schema) + "." + QuoteName(name)
	target := source
	targetName := name
	if t := strings.TrimSpace(o.Target); t != "" {
		parts, err := SplitIdentifier(t)
		if err != nil {
			return ScriptDataResult{}, fmt.Errorf("target table: %w", err)
		}
		if len(parts) == 1 {
			parts = []string{schema, parts[0]}
		}
		quoted := make([]string, len(parts))
		for i, p := range parts {
			quoted[i] = QuoteName(p)
		}
		target = strings.Join(quoted, ".")
		targetName = parts[len(parts)-1]
	}
	if o.RowsPerInsert < 1 {
		o.RowsPerInsert = 1
	}
	if o.RowsPerInsert > 1000 { // T-SQL limit for a VALUES list
		o.RowsPerInsert = 1000
	}

	cols, err := insertableColumns(ctx, db, source, o.IncludeIdentity)
	if err != nil {
		return ScriptDataResult{}, err
	}
	if len(cols) == 0 {
		return ScriptDataResult{}, errors.New("table has no insertable columns")
	}

	orderBy := strings.TrimSpace(o.OrderBy)
	if orderBy == "" {
		orderBy, err = primaryKeyOrder(ctx, db, source)
		if err != nil {
			return ScriptDataResult{}, err
		}
	}

	limit := o.Top
	if limit <= 0 || limit > MaxScriptRows {
		limit = MaxScriptRows
	}
	var q strings.Builder
	fmt.Fprintf(&q, "SELECT TOP (%d) ", limit+1) // +1 detects truncation
	for i, c := range cols {
		if i > 0 {
			q.WriteString(", ")
		}
		q.WriteString(QuoteName(c.name))
	}
	fmt.Fprintf(&q, " FROM %s", source)
	if w := strings.TrimSpace(o.Where); w != "" {
		fmt.Fprintf(&q, " WHERE (%s\n)", w)
	}
	if orderBy != "" {
		fmt.Fprintf(&q, " ORDER BY %s", orderBy)
	}

	rows, err := db.QueryContext(ctx, q.String())
	if err != nil {
		return ScriptDataResult{}, fmt.Errorf("reading data: %w", err)
	}
	defer rows.Close()

	colList := make([]string, len(cols))
	for i, c := range cols {
		colList[i] = QuoteName(c.name)
	}
	insertHead := fmt.Sprintf("INSERT INTO %s (%s) VALUES", target, strings.Join(colList, ", "))

	var body strings.Builder
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	res := ScriptDataResult{Target: target}
	inStmt := 0
	for rows.Next() {
		if res.Rows >= limit {
			res.Truncated = o.Top <= 0 || o.Top > MaxScriptRows
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return ScriptDataResult{}, err
		}
		if inStmt == 0 {
			body.WriteString(insertHead)
			if o.RowsPerInsert > 1 {
				body.WriteString("\n    ")
			} else {
				body.WriteByte(' ')
			}
		} else {
			body.WriteString(",\n    ")
		}
		body.WriteByte('(')
		for i, v := range vals {
			if i > 0 {
				body.WriteString(", ")
			}
			body.WriteString(SQLLiteral(v, cols[i]))
		}
		body.WriteByte(')')
		res.Rows++
		inStmt++
		if inStmt == o.RowsPerInsert {
			body.WriteString(";\n")
			inStmt = 0
		}
		if res.Rows%5000 == 0 && ctx.Err() != nil {
			return ScriptDataResult{}, ctx.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return ScriptDataResult{}, err
	}
	if inStmt > 0 {
		body.WriteString(";\n")
	}

	hasIdentity := false
	for _, c := range cols {
		hasIdentity = hasIdentity || c.identity
	}

	var sb strings.Builder
	var dbName string
	_ = db.QueryRowContext(ctx, "SELECT DB_NAME()").Scan(&dbName)
	fmt.Fprintf(&sb, "-- Data from %s.%s\n", QuoteName(dbName), source)
	fmt.Fprintf(&sb, "-- %d row(s)", res.Rows)
	if o.Top > 0 {
		fmt.Fprintf(&sb, ", TOP %d", o.Top)
	}
	if w := strings.TrimSpace(o.Where); w != "" {
		fmt.Fprintf(&sb, ", WHERE %s", strings.Join(strings.Fields(w), " "))
	}
	if orderBy != "" {
		fmt.Fprintf(&sb, ", ORDER BY %s", strings.Join(strings.Fields(orderBy), " "))
	}
	fmt.Fprintf(&sb, "\n-- Generated by SQL Studio on %s\n", time.Now().Format("2006-01-02 15:04:05"))
	if res.Truncated {
		fmt.Fprintf(&sb, "-- NOTE: limited to the first %d rows. Use a filter or TOP to script the rest.\n", MaxScriptRows)
	}
	sb.WriteString("\n")

	if o.IncludeCreate {
		all, err := Columns(ctx, db, schema, name)
		if err != nil {
			return ScriptDataResult{}, err
		}
		sb.WriteString(createTableScript(all, target, targetName))
		sb.WriteString("\n")
	}

	sb.WriteString("SET NOCOUNT ON;\n")
	if hasIdentity {
		fmt.Fprintf(&sb, "SET IDENTITY_INSERT %s ON;\n", target)
	}
	sb.WriteString("\n")
	if res.Rows == 0 {
		sb.WriteString("-- No rows matched.\n")
	} else {
		sb.WriteString(body.String())
	}
	if hasIdentity {
		fmt.Fprintf(&sb, "\nSET IDENTITY_INSERT %s OFF;\n", target)
	}
	sb.WriteString("GO\n")
	res.Script = sb.String()
	return res, nil
}

func insertableColumns(ctx context.Context, db *sql.DB, source string, includeIdentity bool) ([]dataColumn, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.name, ISNULL(TYPE_NAME(c.system_type_id), t.name), t.is_assembly_type, t.name,
			c.is_identity, c.is_computed, c.system_type_id,
			ISNULL(CAST(COLUMNPROPERTY(c.object_id, c.name, 'GeneratedAlwaysType') AS int), 0)
		FROM sys.columns c
		JOIN sys.types t ON t.user_type_id = c.user_type_id
		WHERE c.object_id = OBJECT_ID(@p1)
		ORDER BY c.column_id`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dataColumn
	found := false
	for rows.Next() {
		found = true
		var (
			c                  dataColumn
			computed           bool
			sysType, generated int
		)
		if err := rows.Scan(&c.name, &c.baseType, &c.udt, &c.udtName, &c.identity, &computed, &sysType, &generated); err != nil {
			return nil, err
		}
		c.baseType = strings.ToLower(c.baseType)
		// Values for these columns are generated by the server and cannot be inserted.
		if computed || sysType == 189 /* timestamp/rowversion */ || generated != 0 {
			continue
		}
		if c.identity && !includeIdentity {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("object %s not found", source)
	}
	return out, nil
}

func primaryKeyOrder(ctx context.Context, db *sql.DB, source string) (string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.name, ic.is_descending_key
		FROM sys.indexes i
		JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE i.object_id = OBJECT_ID(@p1) AND i.is_primary_key = 1
		ORDER BY ic.key_ordinal`, source)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var name string
		var desc bool
		if err := rows.Scan(&name, &desc); err != nil {
			return "", err
		}
		p := QuoteName(name)
		if desc {
			p += " DESC"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", "), rows.Err()
}

// SQLLiteral renders a scanned value as a T-SQL literal for the column's type.
func SQLLiteral(v any, c dataColumn) string {
	if v == nil {
		return "NULL"
	}
	if c.udt {
		if b, ok := v.([]byte); ok {
			return fmt.Sprintf("CAST(0x%s AS %s)", strings.ToUpper(hex.EncodeToString(b)), c.udtName)
		}
	}
	switch x := v.(type) {
	case bool:
		if x {
			return "1"
		}
		return "0"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if c.baseType == "real" {
			return strconv.FormatFloat(x, 'g', -1, 32)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case []byte:
		switch c.baseType {
		case "decimal", "numeric", "money", "smallmoney":
			return string(x)
		case "uniqueidentifier":
			var u mssql.UniqueIdentifier
			if err := u.Scan(x); err == nil {
				return "'" + u.String() + "'"
			}
		}
		if len(x) == 0 {
			return "0x"
		}
		return "0x" + strings.ToUpper(hex.EncodeToString(x))
	case time.Time:
		var s string
		switch c.baseType {
		case "date":
			s = x.Format("2006-01-02")
		case "time":
			s = x.Format("15:04:05.0000000")
		case "datetime":
			s = x.Format("2006-01-02T15:04:05.000")
		case "smalldatetime":
			s = x.Format("2006-01-02T15:04:00")
		case "datetimeoffset":
			s = x.Format("2006-01-02T15:04:05.0000000-07:00")
		default: // datetime2, sql_variant
			s = x.Format("2006-01-02T15:04:05.0000000")
		}
		return "'" + s + "'"
	case string:
		return stringLiteral(x, c.baseType)
	default:
		return stringLiteral(fmt.Sprint(x), c.baseType)
	}
}

func stringLiteral(s, baseType string) string {
	esc := "'" + strings.ReplaceAll(s, "'", "''") + "'"
	switch baseType {
	case "char", "varchar", "text":
		// Keep plain literals for ASCII; use N'' when the text needs Unicode to survive.
		for i := 0; i < len(s); i++ {
			if s[i] >= utf8.RuneSelf {
				return "N" + esc
			}
		}
		return esc
	}
	return "N" + esc
}

// SplitIdentifier splits a possibly-quoted multi-part name such as
// [dbo].[My Table], dbo.Orders or "sales"."Order Lines" into its parts.
func SplitIdentifier(s string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	i := 0
	expectSep := false
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == '.':
			if !expectSep && cur.Len() == 0 {
				return nil, fmt.Errorf("invalid name %q", s)
			}
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
			expectSep = false
			i++
		case ch == '[' || ch == '"':
			if cur.Len() > 0 && strings.TrimSpace(cur.String()) != "" {
				return nil, fmt.Errorf("invalid name %q", s)
			}
			closer := byte(']')
			if ch == '"' {
				closer = '"'
			}
			i++
			var q strings.Builder
			closed := false
			for i < len(s) {
				if s[i] == closer {
					if i+1 < len(s) && s[i+1] == closer {
						q.WriteByte(closer)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				q.WriteByte(s[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted name in %q", s)
			}
			cur.Reset()
			cur.WriteString(q.String())
			expectSep = true
			// skip whitespace until '.' or end
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			if i < len(s) && s[i] != '.' {
				return nil, fmt.Errorf("invalid name %q", s)
			}
		default:
			if expectSep {
				return nil, fmt.Errorf("invalid name %q", s)
			}
			cur.WriteByte(ch)
			i++
		}
	}
	last := strings.TrimSpace(cur.String())
	if last == "" {
		return nil, fmt.Errorf("invalid name %q", s)
	}
	parts = append(parts, last)
	if len(parts) > 3 {
		return nil, fmt.Errorf("name %q has too many parts (use database.schema.table at most)", s)
	}
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("invalid name %q", s)
		}
	}
	return parts, nil
}
