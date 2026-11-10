package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"
)

// Validation errors that the estimated-plan pass reports (objects that do
// not exist). Everything else is left to the NOEXEC pass.
var missingObjectErrors = map[int32]bool{
	208:  true, // Invalid object name
	2812: true, // Could not find stored procedure
	4121: true, // Cannot find either column ... or the user-defined function
}

var createdObjectRe = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+ALTER\s+)?(?:TABLE|VIEW|PROC|PROCEDURE|FUNCTION|SYNONYM|TYPE|SEQUENCE)\s+((?:\[[^\]]+\]|"[^"]+"|[\w@#$]+)(?:\s*\.\s*(?:\[[^\]]+\]|"[^"]+"|[\w@#$]+))*)`)
var objectNameInMsgRe = regexp.MustCompile(`'([^']+)'`)

// normalizeName lowercases and strips brackets/quotes and the schema, e.g.
// "[dbo].[My Table]" -> "my table".
func normalizeName(s string) string {
	parts := strings.Split(s, ".")
	last := strings.TrimSpace(parts[len(parts)-1])
	last = strings.Trim(last, `[]"`)
	return strings.ToLower(last)
}

// Validate checks a script without running it, on a throwaway connection:
//  1. SET NOEXEC ON compiles each batch: syntax errors and invalid columns.
//  2. SET SHOWPLAN_XML ON builds estimated plans: missing tables, views and
//     procedures (which NOEXEC skips because of deferred name resolution).
//     Temp objects and objects created earlier in the script are ignored.
//
// Nothing is executed. The connection is discarded afterwards so the
// session options can never leak into a pooled connection.
func Validate(ctx context.Context, pool *sql.DB, script string) ([]Message, error) {
	batches := SplitBatches(script)
	if len(batches) == 0 {
		return []Message{{Kind: "info", Text: "Nothing to validate.", Time: nowMs()}}, nil
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, friendlyError(err)
	}
	defer func() {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn }) // discard, never reuse
		_ = conn.Close()
	}()

	var msgs []Message
	errorsFound := 0
	report := func(b Batch, me mssql.Error) {
		line := 0
		if me.LineNo > 0 {
			line = b.StartLine + int(me.LineNo) - 1
		}
		errorsFound++
		msgs = append(msgs, Message{
			Kind:   "error",
			Text:   fmt.Sprintf("Msg %d, Level %d, State %d, Line %d\n%s", me.Number, me.Class, me.State, line, me.Message),
			Line:   line,
			Number: int(me.Number),
			Time:   nowMs(),
		})
	}
	serverErrors := func(err error) []mssql.Error {
		var me mssql.Error
		if !errors.As(err, &me) {
			return nil
		}
		if len(me.All) > 0 {
			return me.All
		}
		return []mssql.Error{me}
	}
	drain := func(rows *sql.Rows) error {
		defer rows.Close()
		for {
			for rows.Next() {
			}
			if !rows.NextResultSet() {
				break
			}
		}
		return rows.Err()
	}

	// Pass 1: compile only.
	if _, err := conn.ExecContext(ctx, "SET NOEXEC ON"); err != nil {
		return nil, friendlyError(err)
	}
	for _, b := range batches {
		rows, err := conn.QueryContext(ctx, b.Text)
		if err == nil {
			err = drain(rows)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			errs := serverErrors(err)
			if errs == nil {
				return nil, friendlyError(err)
			}
			for _, e := range errs {
				if e.Class > 10 {
					report(b, e)
				}
			}
		}
	}
	_, _ = conn.ExecContext(ctx, "SET NOEXEC OFF")
	if errorsFound > 0 {
		return msgs, nil
	}

	// Pass 2: estimated plans resolve object names.
	created := map[string]bool{}
	for _, m := range createdObjectRe.FindAllStringSubmatch(script, -1) {
		created[normalizeName(m[1])] = true
	}
	checkedObjects := true
	if _, err := conn.ExecContext(ctx, "SET SHOWPLAN_XML ON"); err != nil {
		checkedObjects = false
	} else {
		for _, b := range batches {
			rows, err := conn.QueryContext(ctx, b.Text)
			if err == nil {
				err = drain(rows)
			}
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			errs := serverErrors(err)
			if errs == nil {
				return nil, friendlyError(err)
			}
			for _, e := range errs {
				if e.Number == 262 { // SHOWPLAN permission denied
					checkedObjects = false
					break
				}
				if !missingObjectErrors[e.Number] {
					continue
				}
				if m := objectNameInMsgRe.FindStringSubmatch(e.Message); m != nil {
					name := normalizeName(m[1])
					if strings.HasPrefix(name, "#") || created[name] {
						continue // temp object or created earlier in this script
					}
				}
				report(b, e)
			}
			if !checkedObjects {
				break
			}
		}
		_, _ = conn.ExecContext(ctx, "SET SHOWPLAN_XML OFF")
	}
	if errorsFound > 0 {
		return msgs, nil
	}
	text := fmt.Sprintf("✓ Query is valid — syntax, columns and object names checked (%d batch%s). Nothing was executed.", len(batches), plural(len(batches), "", "es"))
	if !checkedObjects {
		text = fmt.Sprintf("✓ Syntax and columns are valid (%d batch%s). Object names were not checked: that needs the SHOWPLAN permission. Nothing was executed.", len(batches), plural(len(batches), "", "es"))
	}
	return append(msgs, Message{Kind: "info", Text: text, Time: nowMs()}), nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
