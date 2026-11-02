package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-sql/sqlexp"
	mssql "github.com/microsoft/go-mssqldb"
)

// Column describes a result-set column.
type Column struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Numeric bool   `json:"numeric"`
}

// Message is an informational / error line for the Messages pane.
type Message struct {
	Kind   string `json:"kind"` // "info", "error", "rows"
	Text   string `json:"text"`
	Line   int    `json:"line,omitempty"` // 1-based line in the editor, when known
	Number int    `json:"number,omitempty"`
	Time   int64  `json:"time"` // unix ms
}

// Sink receives streaming execution output.
type Sink interface {
	ResultSet(index int, cols []Column)
	Rows(index int, rows [][]any)
	ResultDone(index int, rowCount int, truncated bool)
	Message(m Message)
}

// ExecOptions control a single execution.
type ExecOptions struct {
	MaxRows int           // per result set; 0 = unlimited
	Timeout time.Duration // 0 = none
}

// Summary is returned when execution completes.
type Summary struct {
	ElapsedMs  int64  `json:"elapsedMs"`
	Batches    int    `json:"batches"`
	Errors     int    `json:"errors"`
	ResultSets int    `json:"resultSets"`
	Cancelled  bool   `json:"cancelled"`
	Database   string `json:"database"`
}

// Session is a dedicated connection bound to one editor tab, so temp tables,
// SET options and open transactions persist between executions — like SSMS/ADS.
type Session struct {
	ConnectionID string
	Database     string

	pool   *sql.DB
	runMu  sync.Mutex // serialises executions
	mu     sync.Mutex // guards conn, cancel
	conn   *sql.Conn
	cancel context.CancelFunc
}

// Session returns the session for a tab, (re)creating it if the target changed.
func (m *Manager) Session(ctx context.Context, tabID, connID, database string) (*Session, error) {
	m.mu.Lock()
	if s, ok := m.sessions[tabID]; ok {
		if s.ConnectionID == connID && (database == "" || strings.EqualFold(s.Database, database)) {
			m.mu.Unlock()
			return s, nil
		}
		delete(m.sessions, tabID)
		m.mu.Unlock()
		s.close()
	} else {
		m.mu.Unlock()
	}

	pool, err := m.Pool(ctx, connID, database)
	if err != nil {
		return nil, err
	}
	s := &Session{ConnectionID: connID, Database: database, pool: pool}
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[tabID] = s
	m.mu.Unlock()
	return s, nil
}

// CloseSession closes a tab's dedicated connection (rolling back open transactions).
func (m *Manager) CloseSession(tabID string) {
	m.mu.Lock()
	s := m.sessions[tabID]
	delete(m.sessions, tabID)
	m.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// Cancel stops the running execution for a tab, if any.
func (m *Manager) Cancel(tabID string) bool {
	m.mu.Lock()
	s := m.sessions[tabID]
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		return true
	}
	return false
}

func (s *Session) ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return nil
	}
	c, err := s.pool.Conn(ctx)
	if err != nil {
		return friendlyError(err)
	}
	s.conn = c
	if s.Database == "" {
		_ = c.QueryRowContext(ctx, "SELECT DB_NAME()").Scan(&s.Database)
	}
	return nil
}

func (s *Session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

func (s *Session) dropConn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn }) // discard, don't return to pool
		_ = s.conn.Close()
		s.conn = nil
	}
}

// Execute runs a script (possibly multiple GO-separated batches), streaming
// results and messages to sink. Like SSMS, a failing batch does not stop
// subsequent batches.
func (s *Session) Execute(parent context.Context, script string, opts ExecOptions, sink Sink) Summary {
	s.runMu.Lock()
	defer s.runMu.Unlock()

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
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}()

	sum := Summary{}
	if err := s.ensure(ctx); err != nil {
		sink.Message(Message{Kind: "error", Text: err.Error(), Time: nowMs()})
		sum.Errors++
		sum.ElapsedMs = time.Since(start).Milliseconds()
		return sum
	}

	ex := &executor{s: s, sink: sink, opts: opts, sum: &sum}
	batches := SplitBatches(script)
	for _, b := range batches {
		for r := 0; r < b.Repeat; r++ {
			if ctx.Err() != nil {
				break
			}
			sum.Batches++
			ex.runBatch(ctx, b)
		}
	}
	if ctx.Err() != nil {
		sum.Cancelled = true
		text := "Query was cancelled by user."
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			text = "Query timeout expired."
		}
		sink.Message(Message{Kind: "error", Text: text, Time: nowMs()})
	} else if sum.Errors == 0 && !ex.anyOutput {
		sink.Message(Message{Kind: "info", Text: "Commands completed successfully.", Time: nowMs()})
	}

	// Track the current database (USE statements change it on SQL Server).
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		qctx, qcancel := context.WithTimeout(context.Background(), 5*time.Second)
		var dbName string
		if err := conn.QueryRowContext(qctx, "SELECT DB_NAME()").Scan(&dbName); err == nil {
			s.Database = dbName
		} else {
			s.dropConn()
		}
		qcancel()
	}
	sum.Database = s.Database
	sum.ElapsedMs = time.Since(start).Milliseconds()
	return sum
}

type executor struct {
	s         *Session
	sink      Sink
	opts      ExecOptions
	sum       *Summary
	anyOutput bool
}

func (e *executor) runBatch(ctx context.Context, b Batch) {
	e.s.mu.Lock()
	conn := e.s.conn
	e.s.mu.Unlock()
	if conn == nil {
		if err := e.s.ensure(ctx); err != nil {
			e.error(err, b)
			return
		}
		e.s.mu.Lock()
		conn = e.s.conn
		e.s.mu.Unlock()
	}

	retmsg := &sqlexp.ReturnMessage{}
	rows, err := conn.QueryContext(ctx, b.Text, retmsg)
	if err != nil {
		e.error(err, b)
		return
	}
	defer rows.Close()

	for active := true; active; {
		switch m := retmsg.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			e.anyOutput = true
			e.sink.Message(Message{Kind: "info", Text: m.Message.String(), Time: nowMs()})
		case sqlexp.MsgNext:
			e.readResultSet(ctx, rows, b)
		case sqlexp.MsgRowsAffected:
			e.anyOutput = true
			noun := "rows"
			if m.Count == 1 {
				noun = "row"
			}
			e.sink.Message(Message{Kind: "rows", Text: fmt.Sprintf("(%d %s affected)", m.Count, noun), Time: nowMs()})
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		case sqlexp.MsgError:
			e.error(m.Error, b)
		}
	}
	if err := rows.Err(); err != nil && ctx.Err() == nil {
		e.error(err, b)
	}
}

func (e *executor) readResultSet(ctx context.Context, rows *sql.Rows, b Batch) {
	types, err := rows.ColumnTypes()
	if err != nil {
		e.error(err, b)
		return
	}
	idx := e.sum.ResultSets
	e.sum.ResultSets++
	e.anyOutput = true
	cols := make([]Column, len(types))
	dbTypes := make([]string, len(types))
	for i, t := range types {
		dbTypes[i] = t.DatabaseTypeName()
		cols[i] = Column{Name: t.Name(), Type: strings.ToLower(dbTypes[i]), Numeric: IsNumericType(dbTypes[i])}
	}
	e.sink.ResultSet(idx, cols)

	vals := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	const chunk = 500
	buf := make([][]any, 0, chunk)
	lastFlush := time.Now()
	count, truncated := 0, false
	for rows.Next() {
		if e.opts.MaxRows > 0 && count >= e.opts.MaxRows {
			truncated = true
			continue // drain remaining rows so the next result set can be read
		}
		if err := rows.Scan(ptrs...); err != nil {
			e.error(err, b)
			break
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = FormatValue(v, dbTypes[i])
		}
		buf = append(buf, row)
		count++
		if len(buf) >= chunk || time.Since(lastFlush) > 150*time.Millisecond {
			e.sink.Rows(idx, buf)
			buf = make([][]any, 0, chunk)
			lastFlush = time.Now()
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(buf) > 0 {
		e.sink.Rows(idx, buf)
	}
	e.sink.ResultDone(idx, count, truncated)
}

func (e *executor) error(err error, b Batch) {
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return // reported once by Execute
	}
	e.anyOutput = true
	var me mssql.Error
	if errors.As(err, &me) {
		all := me.All
		if len(all) == 0 {
			all = []mssql.Error{me}
		}
		for _, x := range all {
			if x.Class <= 10 { // informational, already delivered as a notice
				continue
			}
			e.sum.Errors++
			line := 0
			if x.LineNo > 0 {
				line = b.StartLine + int(x.LineNo) - 1
			}
			head := fmt.Sprintf("Msg %d, Level %d, State %d, Line %d", x.Number, x.Class, x.State, line)
			if x.ProcName != "" {
				head = fmt.Sprintf("Msg %d, Level %d, State %d, Procedure %s, Line %d", x.Number, x.Class, x.State, x.ProcName, x.LineNo)
				line = 0
			}
			e.sink.Message(Message{Kind: "error", Text: head + "\n" + x.Message, Line: line, Number: int(x.Number), Time: nowMs()})
		}
		return
	}
	e.sum.Errors++
	e.sink.Message(Message{Kind: "error", Text: friendlyError(err).Error(), Time: nowMs()})
	// Non-server errors usually mean the connection is unusable; reconnect next time.
	e.s.dropConn()
}

func nowMs() int64 { return time.Now().UnixMilli() }
