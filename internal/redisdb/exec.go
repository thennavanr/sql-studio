package redisdb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"

	"github.com/thennavanr/sql-studio/internal/engine"
)

// Command is one parsed line of a script.
type Command struct {
	Args []string
	Line int
}

// Name returns the upper-cased command name (with subcommand for container
// commands such as CONFIG GET).
func (c Command) Name() string {
	if len(c.Args) == 0 {
		return ""
	}
	return strings.ToUpper(c.Args[0])
}

// ParseError reports a syntax problem with its line.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// ParseScript splits a script into commands the way redis-cli does: one
// command per line, arguments separated by spaces, "double quoted" strings
// with escapes (\n \t \" \\ \xHH) and 'single quoted' literal strings.
// Lines starting with # or // are comments.
func ParseScript(script string) ([]Command, error) {
	var out []Command
	for i, line := range strings.Split(script, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		args, err := splitArgs(t)
		if err != nil {
			return nil, &ParseError{Line: i + 1, Msg: err.Error()}
		}
		if len(args) > 0 {
			out = append(out, Command{Args: args, Line: i + 1})
		}
	}
	return out, nil
}

func splitArgs(s string) ([]string, error) {
	var args []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		var sb strings.Builder
		switch s[i] {
		case '"':
			i++
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '"' {
					i++
					closed = true
					break
				}
				if c == '\\' && i+1 < len(s) {
					i++
					switch s[i] {
					case 'n':
						sb.WriteByte('\n')
					case 'r':
						sb.WriteByte('\r')
					case 't':
						sb.WriteByte('\t')
					case 'b':
						sb.WriteByte('\b')
					case 'a':
						sb.WriteByte('\a')
					case 'x':
						if i+2 < len(s) {
							if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
								sb.WriteByte(byte(n))
								i += 2
								break
							}
						}
						sb.WriteByte('x')
					default:
						sb.WriteByte(s[i])
					}
					i++
					continue
				}
				sb.WriteByte(c)
				i++
			}
			if !closed {
				return nil, errors.New("unterminated \"quoted\" string")
			}
		case '\'':
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) && s[i+1] == '\'' {
					sb.WriteByte('\'')
					i += 2
					continue
				}
				if s[i] == '\'' {
					i++
					closed = true
					break
				}
				sb.WriteByte(s[i])
				i++
			}
			if !closed {
				return nil, errors.New("unterminated 'quoted' string")
			}
		default:
			for i < len(s) && s[i] != ' ' && s[i] != '\t' {
				sb.WriteByte(s[i])
				i++
			}
		}
		if i < len(s) && s[i] != ' ' && s[i] != '\t' {
			return nil, errors.New("closing quote must be followed by a space")
		}
		args = append(args, sb.String())
	}
	return args, nil
}

// QuoteArg quotes an argument for display in a command when needed.
func QuoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\\n\r") && utf8.ValidString(s) {
		return s
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// Clients returns the client for a database number (used for SELECT).
type Clients func(ctx context.Context, db int) (*redis.Client, error)

// Execute runs a script, emitting one result set per command that returns
// data. Like the MongoDB runner it stops at the first error.
func Execute(parent context.Context, clients Clients, db int, script string, opts engine.ExecOptions, sink engine.Sink) engine.Summary {
	start := time.Now()
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if opts.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, opts.Timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	sum := engine.Summary{Database: DBName(db)}
	msg := func(kind, text string, line int) {
		sink.Message(engine.Message{Kind: kind, Text: text, Line: line, Time: time.Now().UnixMilli()})
	}
	cmds, err := ParseScript(script)
	if err != nil {
		var pe *ParseError
		line := 0
		if errors.As(err, &pe) {
			line = pe.Line
		}
		msg("error", "Syntax error: "+err.Error(), line)
		sum.Errors = 1
		sum.ElapsedMs = time.Since(start).Milliseconds()
		return sum
	}
	maxRows := opts.MaxRows
	if maxRows <= 0 {
		maxRows = 100000
	}
	results := 0
	for _, c := range cmds {
		if ctx.Err() != nil {
			break
		}
		sum.Batches++
		name := c.Name()
		if name == "SELECT" {
			if len(c.Args) != 2 {
				msg("error", fmt.Sprintf("Line %d: usage: SELECT <db>", c.Line), c.Line)
				sum.Errors++
				break
			}
			n, ok := ParseDBName(c.Args[1])
			if !ok {
				msg("error", fmt.Sprintf("Line %d: invalid database %q", c.Line, c.Args[1]), c.Line)
				sum.Errors++
				break
			}
			if _, err := clients(ctx, n); err != nil {
				msg("error", fmt.Sprintf("Line %d: %v", c.Line, err), c.Line)
				sum.Errors++
				break
			}
			db = n
			msg("info", "OK — switched to "+DBName(n), c.Line)
			continue
		}
		if blockedCommands[name] {
			msg("error", fmt.Sprintf("Line %d: %s is not supported here (it would tie up the connection). Use a terminal with redis-cli.", c.Line, name), c.Line)
			sum.Errors++
			break
		}
		cl, err := clients(ctx, db)
		if err != nil {
			msg("error", err.Error(), c.Line)
			sum.Errors++
			break
		}
		args := make([]any, len(c.Args))
		for i, a := range c.Args {
			args[i] = a
		}
		res, err := cl.Do(ctx, args...).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			if ctx.Err() != nil {
				break
			}
			msg("error", fmt.Sprintf("Line %d: %s", c.Line, friendlyError(err)), c.Line)
			sum.Errors++
			break
		}
		if errors.Is(err, redis.Nil) {
			res = nil
		}
		if s, ok := res.(string); ok && (s == "OK" || s == "QUEUED") {
			msg("info", s, c.Line)
			continue
		}
		cols, rows, note := tabulate(name, res, maxRows)
		idx := results
		results++
		sink.ResultSet(idx, cols)
		for i := 0; i < len(rows); i += 500 {
			end := min(i+500, len(rows))
			sink.Rows(idx, rows[i:end])
		}
		sink.ResultDone(idx, len(rows), false)
		text := describe(res)
		if note != "" {
			text += " — " + note
		}
		msg("rows", text, c.Line)
	}
	if ctx.Err() != nil {
		sum.Cancelled = true
		msg("error", "Command was cancelled.", 0)
	} else if sum.Errors == 0 && results == 0 && len(cmds) == 0 {
		msg("info", "Nothing to run.", 0)
	}
	sum.ResultSets = results
	sum.Database = DBName(db)
	sum.ElapsedMs = time.Since(start).Milliseconds()
	return sum
}

// Commands that block or switch the connection into a special mode.
var blockedCommands = map[string]bool{
	"SUBSCRIBE": true, "PSUBSCRIBE": true, "SSUBSCRIBE": true, "MONITOR": true, "MULTI": true,
	"EXEC": true, "WATCH": true, "SYNC": true, "PSYNC": true, "RESET": true, "QUIT": true,
}

// DangerousCommands need confirmation in the UI before running.
var DangerousCommands = []string{"FLUSHALL", "FLUSHDB", "SHUTDOWN", "SWAPDB", "DEBUG", "MIGRATE", "SCRIPT FLUSH", "FUNCTION FLUSH", "CONFIG SET", "CONFIG RESETSTAT", "CLIENT KILL", "REPLICAOF", "SLAVEOF", "FAILOVER"}

func describe(v any) string {
	switch x := v.(type) {
	case nil:
		return "(nil)"
	case int64:
		return fmt.Sprintf("(integer) %d", x)
	case []any:
		return fmt.Sprintf("(%d item%s)", len(x), plural(len(x)))
	case map[any]any:
		return fmt.Sprintf("(%d field%s)", len(x), plural(len(x)))
	}
	return "(1 value)"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Cell renders a reply value for a grid cell; nil stays nil (NULL).
func Cell(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return printable(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case error:
		return "(error) " + x.Error()
	}
	return compact(v)
}

// printable shows binary values the way redis-cli does: valid UTF-8 text is
// kept, control characters and invalid bytes become \xNN escapes.
func printable(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' || r == 0x7f }) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1, r < 0x20 && r != '\n' && r != '\t', r == 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, s[i])
		default:
			sb.WriteString(s[i : i+size])
		}
		i += size
	}
	return sb.String()
}

// compact renders nested replies as one line of text.
func compact(v any) string {
	switch x := v.(type) {
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = compactScalar(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[any]any:
		keys := sortedKeys(x)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = compactScalar(k) + ": " + compactScalar(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func compactScalar(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(printable(x))
	case nil:
		return "nil"
	case []any, map[any]any:
		return compact(x)
	}
	c := Cell(v)
	if s, ok := c.(string); ok {
		return s
	}
	return fmt.Sprint(c)
}

func sortedKeys(m map[any]any) []any {
	keys := make([]any, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	return keys
}

func isScalar(v any) bool {
	switch v.(type) {
	case []any, map[any]any:
		return false
	}
	return true
}

var pairCommands = map[string][2]string{
	"HGETALL": {"field", "value"}, "CONFIG": {"parameter", "value"}, "HRANDFIELD": {"field", "value"},
}
var scoreCommands = map[string]bool{
	"ZRANGE": true, "ZREVRANGE": true, "ZRANGEBYSCORE": true, "ZREVRANGEBYSCORE": true, "ZRANDMEMBER": true,
	"ZPOPMIN": true, "ZPOPMAX": true, "ZUNION": true, "ZINTER": true, "ZDIFF": true,
}

// tabulate turns a reply into grid columns and rows.
func tabulate(name string, v any, maxRows int) ([]engine.Column, [][]any, string) {
	col := func(n string, numeric bool) engine.Column {
		return engine.Column{Name: n, Type: "value", Numeric: numeric}
	}
	note := ""
	switch x := v.(type) {
	case map[any]any: // RESP3 map (HGETALL, CONFIG GET, ...)
		names := [2]string{"field", "value"}
		if p, ok := pairCommands[name]; ok {
			names = p
		}
		keys := sortedKeys(x)
		rows := make([][]any, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, []any{Cell(k), Cell(x[k])})
		}
		return []engine.Column{col(names[0], false), col(names[1], false)}, limit(rows, maxRows, &note), note
	case []any:
		// SCAN family: [cursor, [items]]
		if strings.HasSuffix(name, "SCAN") && len(x) == 2 {
			if items, ok := x[1].([]any); ok {
				note = fmt.Sprintf("next cursor %v (0 = finished)", Cell(x[0]))
				switch name {
				case "HSCAN":
					return pairs(items, "field", "value", maxRows, note)
				case "ZSCAN":
					return pairs(items, "member", "score", maxRows, note)
				}
				x = items
			}
		}
		// RESP2 flat pairs for commands known to return them.
		if p, ok := pairCommands[name]; ok && len(x)%2 == 0 && allScalar(x) {
			return pairs(x, p[0], p[1], maxRows, note)
		}
		// Array of uniform small tuples (ZRANGE … WITHSCORES in RESP3, XRANGE entries, ...).
		if width := tupleWidth(x); width > 0 {
			cols := []engine.Column{col("#", true)}
			for i := 0; i < width; i++ {
				n := strconv.Itoa(i + 1)
				if scoreCommands[name] && width == 2 {
					n = [2]string{"member", "score"}[i]
				} else if (name == "XRANGE" || name == "XREVRANGE") && width == 2 {
					n = [2]string{"id", "fields"}[i]
				}
				cols = append(cols, col(n, false))
			}
			rows := make([][]any, len(x))
			for i, e := range x {
				t := e.([]any)
				row := []any{strconv.Itoa(i + 1)}
				for j := 0; j < width; j++ {
					if j < len(t) {
						if m, ok := t[j].([]any); ok && (name == "XRANGE" || name == "XREVRANGE") {
							row = append(row, streamFields(m))
						} else {
							row = append(row, Cell(t[j]))
						}
					} else {
						row = append(row, nil)
					}
				}
				rows[i] = row
			}
			return cols, limit(rows, maxRows, &note), note
		}
		rows := make([][]any, len(x))
		numeric := len(x) > 0
		for i, e := range x {
			rows[i] = []any{strconv.Itoa(i + 1), Cell(e)}
			if _, ok := e.(int64); !ok {
				numeric = false
			}
		}
		return []engine.Column{col("#", true), col("value", numeric)}, limit(rows, maxRows, &note), note
	}
	_, isNum := v.(int64)
	if _, f := v.(float64); f {
		isNum = true
	}
	return []engine.Column{col("value", isNum)}, [][]any{{Cell(v)}}, note
}

func streamFields(m []any) string {
	parts := make([]string, 0, len(m)/2)
	for i := 0; i+1 < len(m); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", Cell(m[i]), Cell(m[i+1])))
	}
	return strings.Join(parts, "  ")
}

func pairs(items []any, a, b string, maxRows int, note string) ([]engine.Column, [][]any, string) {
	rows := make([][]any, 0, len(items)/2)
	for i := 0; i+1 < len(items); i += 2 {
		rows = append(rows, []any{Cell(items[i]), Cell(items[i+1])})
	}
	return []engine.Column{{Name: a, Type: "value"}, {Name: b, Type: "value"}}, limit(rows, maxRows, &note), note
}

func allScalar(x []any) bool {
	for _, e := range x {
		if !isScalar(e) {
			return false
		}
	}
	return true
}

// tupleWidth returns n when every element is an array of 1..n items (n ≤ 8).
func tupleWidth(x []any) int {
	if len(x) == 0 {
		return 0
	}
	width := 0
	for _, e := range x {
		t, ok := e.([]any)
		if !ok || len(t) == 0 || len(t) > 8 {
			return 0
		}
		width = max(width, len(t))
	}
	return width
}

func limit(rows [][]any, maxRows int, note *string) [][]any {
	if len(rows) > maxRows {
		if *note != "" {
			*note += "; "
		}
		*note += fmt.Sprintf("showing the first %d", maxRows)
		return rows[:maxRows]
	}
	return rows
}

// Validate checks a script without running it: syntax, that each command
// exists and that it has a valid number of arguments (via COMMAND INFO).
func Validate(ctx context.Context, cl *redis.Client, script string) []engine.Message {
	now := func() int64 { return time.Now().UnixMilli() }
	cmds, err := ParseScript(script)
	if err != nil {
		line := 0
		var pe *ParseError
		if errors.As(err, &pe) {
			line = pe.Line
		}
		return []engine.Message{{Kind: "error", Text: "Syntax error: " + err.Error(), Line: line, Time: now()}}
	}
	if len(cmds) == 0 {
		return []engine.Message{{Kind: "info", Text: "Nothing to validate.", Time: now()}}
	}
	names := map[string]bool{}
	for _, c := range cmds {
		names[strings.ToLower(c.Args[0])] = true
	}
	args := []any{"COMMAND", "INFO"}
	for n := range names {
		args = append(args, n)
	}
	arity := map[string]int64{}
	checked := false
	if res, err := cl.Do(ctx, args...).Result(); err == nil {
		checked = true
		for _, e := range toSlice(res) {
			info, ok := e.([]any)
			if !ok || len(info) < 2 {
				continue
			}
			n, _ := info[0].(string)
			a, _ := info[1].(int64)
			arity[strings.ToUpper(n)] = a
		}
	}
	var msgs []engine.Message
	errs := 0
	for _, c := range cmds {
		name := c.Name()
		if blockedCommands[name] {
			errs++
			msgs = append(msgs, engine.Message{Kind: "error", Text: fmt.Sprintf("Line %d: %s is not supported here.", c.Line, name), Line: c.Line, Time: now()})
			continue
		}
		if name == "SELECT" {
			if _, ok := ParseDBName(strings.Join(c.Args[1:], "")); len(c.Args) != 2 || !ok {
				errs++
				msgs = append(msgs, engine.Message{Kind: "error", Text: fmt.Sprintf("Line %d: usage: SELECT <db>", c.Line), Line: c.Line, Time: now()})
			}
			continue
		}
		if !checked {
			continue
		}
		a, known := arity[name]
		if !known {
			errs++
			msgs = append(msgs, engine.Message{Kind: "error", Text: fmt.Sprintf("Line %d: unknown command '%s'", c.Line, c.Args[0]), Line: c.Line, Time: now()})
			continue
		}
		n := int64(len(c.Args))
		if (a > 0 && n != a) || (a < 0 && n < -a) {
			want := fmt.Sprintf("%d", a-1)
			if a < 0 {
				want = fmt.Sprintf("at least %d", -a-1)
			}
			errs++
			msgs = append(msgs, engine.Message{Kind: "error", Text: fmt.Sprintf("Line %d: wrong number of arguments for '%s' (expects %s)", c.Line, strings.ToLower(name), want), Line: c.Line, Time: now()})
			continue
		}
		if name == "KEYS" {
			msgs = append(msgs, engine.Message{Kind: "info", Text: fmt.Sprintf("⚠ Line %d: KEYS scans the whole database and can block the server; prefer SCAN 0 MATCH <pattern> COUNT 100.", c.Line), Line: c.Line, Time: now()})
		}
	}
	if errs == 0 {
		text := fmt.Sprintf("✓ Script is valid — %d command%s checked. Nothing was executed.", len(cmds), plural(len(cmds)))
		if !checked {
			text = fmt.Sprintf("✓ Syntax is valid (%d command%s). Command names were not checked: the server does not allow COMMAND INFO. Nothing was executed.", len(cmds), plural(len(cmds)))
		}
		msgs = append(msgs, engine.Message{Kind: "info", Text: text, Time: now()})
	}
	return msgs
}

func toSlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[any]any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			out = append(out, e)
		}
		return out
	}
	return nil
}
