package engine

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thennavanr/sql-studio/internal/store"
)

func TestSplitBatches(t *testing.T) {
	script := "SELECT 1\nGO\n-- GO inside comment\nSELECT 'a\nGO\nb'\n/* \nGO\n */\nSELECT [x\nGO]\ngo 3\nSELECT 2 -- trailing\n  GO  -- comment\n"
	b := SplitBatches(script)
	if len(b) != 3 {
		for i, x := range b {
			t.Logf("%d (line %d x%d): %q", i, x.StartLine, x.Repeat, x.Text)
		}
		t.Fatalf("want 3 batches, got %d", len(b))
	}
	if b[0].StartLine != 1 || strings.TrimSpace(b[0].Text) != "SELECT 1" {
		t.Errorf("batch 0 = %+v", b[0])
	}
	if b[1].StartLine != 3 || b[1].Repeat != 3 || !strings.Contains(b[1].Text, "GO]") {
		t.Errorf("batch 1 = %+v", b[1])
	}
	if b[2].StartLine != 13 || strings.TrimSpace(b[2].Text) != "SELECT 2 -- trailing" {
		t.Errorf("batch 2 = %+v", b[2])
	}
	if got := SplitBatches("GO\n\nGO"); len(got) != 0 {
		t.Errorf("empty batches should be dropped, got %d", len(got))
	}
	if got := SplitBatches("SELECT 'it''s'\nGOTO label"); len(got) != 1 {
		t.Errorf("GOTO is not a separator, got %d", len(got))
	}
}

func TestParseServer(t *testing.T) {
	cases := []struct {
		in       string
		host     string
		port     int
		instance string
	}{
		{"tcp:myserver.database.windows.net,1433", "myserver.database.windows.net", 1433, ""},
		{"myserver.database.windows.net", "myserver.database.windows.net", 0, ""},
		{"localhost:14330", "localhost", 14330, ""},
		{`host\SQLEXPRESS`, "host", 0, "SQLEXPRESS"},
		{" host , 1500 ", "host", 1500, ""},
	}
	for _, c := range cases {
		h, p, i := ParseServer(c.in, 0)
		if h != c.host || p != c.port || i != c.instance {
			t.Errorf("ParseServer(%q) = %q,%d,%q", c.in, h, p, i)
		}
	}
	if !IsAzureHost("x.database.windows.net") || IsAzureHost("localhost") {
		t.Error("IsAzureHost")
	}
}

func TestBuildDSN(t *testing.T) {
	dsn, err := BuildDSN(store.Connection{Server: "tcp:srv.database.windows.net,1433", Auth: store.AuthSQL, User: "me", Password: "p@ss;word"}, "db 1")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	if u.Host != "srv.database.windows.net:1433" || u.User.Username() != "me" || pw != "p@ss;word" {
		t.Errorf("bad dsn %s", dsn)
	}
	if u.Query().Get("database") != "db 1" || u.Query().Get("encrypt") != "true" {
		t.Errorf("bad query %s", dsn)
	}
	dsn, _ = BuildDSN(store.Connection{Server: "srv", Auth: store.AuthEntraInteractive, User: "a@b.com"}, "")
	if strings.Contains(dsn, "@") {
		t.Errorf("entra DSN must not contain user info: %s", dsn)
	}
}

func TestFormatType(t *testing.T) {
	for in, want := range map[[4]any]string{
		{"nvarchar", 100, 0, 0}: "nvarchar(50)",
		{"varchar", -1, 0, 0}:   "varchar(max)",
		{"decimal", 9, 18, 2}:   "decimal(18,2)",
		{"int", 4, 10, 0}:       "int",
	} {
		if got := FormatType(in[0].(string), in[1].(int), in[2].(int), in[3].(int)); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
}

// --- integration tests (require SQLSTUDIO_TEST_SERVER etc.) ---

type recSink struct {
	mu   sync.Mutex
	cols map[int][]Column
	rows map[int][][]any
	msgs []Message
	done map[int]bool
}

func newSink() *recSink {
	return &recSink{cols: map[int][]Column{}, rows: map[int][][]any{}, done: map[int]bool{}}
}
func (s *recSink) ResultSet(i int, c []Column) { s.mu.Lock(); s.cols[i] = c; s.mu.Unlock() }
func (s *recSink) Rows(i int, r [][]any) {
	s.mu.Lock()
	s.rows[i] = append(s.rows[i], r...)
	s.mu.Unlock()
}
func (s *recSink) ResultDone(i, n int, tr bool) {
	s.mu.Lock()
	s.done[i] = tr
	s.mu.Unlock()
}
func (s *recSink) Message(m Message) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }
func (s *recSink) text() string {
	var b strings.Builder
	for _, m := range s.msgs {
		b.WriteString(m.Kind + ": " + m.Text + "\n")
	}
	return b.String()
}

func testConn(t *testing.T) store.Connection {
	server := os.Getenv("SQLSTUDIO_TEST_SERVER")
	if server == "" {
		t.Skip("set SQLSTUDIO_TEST_SERVER, SQLSTUDIO_TEST_USER, SQLSTUDIO_TEST_PASSWORD to run integration tests")
	}
	return store.Connection{
		ID: "test", Server: server, Auth: store.AuthSQL,
		User: os.Getenv("SQLSTUDIO_TEST_USER"), Password: os.Getenv("SQLSTUDIO_TEST_PASSWORD"),
		TrustServerCertificate: true, Database: "master",
	}
}

func TestIntegration(t *testing.T) {
	c := testConn(t)
	ctx := context.Background()
	info, err := Test(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("server %+v", info)

	m := NewManager()
	defer m.Close()
	m.Register(c)
	s, err := m.Session(ctx, "tab1", c.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	script := `IF DB_ID('sqlstudio_test') IS NULL CREATE DATABASE sqlstudio_test;
GO
USE sqlstudio_test;
GO
DROP TABLE IF EXISTS dbo.t;
CREATE TABLE dbo.t (id int IDENTITY PRIMARY KEY, name nvarchar(50) NULL, amount decimal(18,2),
  g uniqueidentifier, d datetime2, b varbinary(10), f bit, big bigint);
INSERT dbo.t (name, amount, g, d, b, f, big) VALUES
  (N'héllo', 12.34, '6F9619FF-8B86-D011-B42D-00C04FC964FF', '2024-01-02T03:04:05.1234567', 0x0102, 1, 9007199254740993),
  (NULL, NULL, NULL, NULL, NULL, 0, NULL);
PRINT 'printed';
SELECT * FROM dbo.t ORDER BY id;
SELECT 1 AS a; SELECT 2 AS b;
SELECT 1/0;
GO
CREATE TABLE #tmp (x int);
`
	sink := newSink()
	sum := s.Execute(ctx, script, ExecOptions{MaxRows: 1000}, sink)
	t.Logf("summary %+v\n%s", sum, sink.text())
	if sum.Database != "sqlstudio_test" {
		t.Errorf("database not tracked: %q", sum.Database)
	}
	if sum.ResultSets != 4 || sum.Errors != 1 {
		t.Errorf("result sets=%d errors=%d", sum.ResultSets, sum.Errors)
	}
	r := sink.rows[0]
	if len(r) != 2 {
		t.Fatalf("rows: %v", r)
	}
	want := []any{"1", "héllo", "12.34", "6F9619FF-8B86-D011-B42D-00C04FC964FF", "2024-01-02 03:04:05.1234567", "0x0102", "1", "9007199254740993"}
	for i, w := range want {
		if r[0][i] != w {
			t.Errorf("col %d: got %v want %v", i, r[0][i], w)
		}
	}
	if r[1][1] != nil {
		t.Errorf("NULL should be nil, got %v", r[1][1])
	}
	txt := sink.text()
	for _, w := range []string{"printed", "(2 rows affected)", "Msg 8134, Level 16", "Line 14"} {
		if !strings.Contains(txt, w) {
			t.Errorf("messages missing %q", w)
		}
	}

	// Temp table persists in the session (same connection).
	sink = newSink()
	sum = s.Execute(ctx, "SELECT COUNT(*) FROM #tmp", ExecOptions{}, sink)
	if sum.Errors != 0 {
		t.Errorf("temp table not visible: %s", sink.text())
	}

	// Max rows truncation still allows later result sets.
	sink = newSink()
	sum = s.Execute(ctx, "SELECT TOP 50 object_id FROM sys.all_objects; SELECT 'next'", ExecOptions{MaxRows: 10}, sink)
	if len(sink.rows[0]) != 10 || !sink.done[0] || len(sink.rows[1]) != 1 {
		t.Errorf("truncation: %d rows, truncated=%v, next=%v", len(sink.rows[0]), sink.done[0], sink.rows[1])
	}

	// Cancellation.
	go func() { time.Sleep(500 * time.Millisecond); m.Cancel("tab1") }()
	sink = newSink()
	start := time.Now()
	sum = s.Execute(ctx, "WAITFOR DELAY '00:00:20'", ExecOptions{}, sink)
	if !sum.Cancelled || time.Since(start) > 5*time.Second {
		t.Errorf("cancel failed: %+v %s", sum, sink.text())
	}
	sink = newSink()
	if sum = s.Execute(ctx, "SELECT 42", ExecOptions{}, sink); sum.Errors != 0 || sink.rows[0][0][0] != "42" {
		t.Errorf("session unusable after cancel: %s", sink.text())
	}

	// Metadata.
	pool, err := m.Pool(ctx, c.ID, "sqlstudio_test")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Objects(ctx, pool)
	if err != nil || len(objs) == 0 {
		t.Fatalf("objects: %v %v", objs, err)
	}
	cols, err := Columns(ctx, pool, "dbo", "t")
	if err != nil || len(cols) != 8 || !cols[0].PK || cols[1].Type != "nvarchar(50)" {
		t.Errorf("columns: %+v %v", cols, err)
	}
	def, err := Definition(ctx, pool, "dbo", "t", "table")
	if err != nil || !strings.Contains(def, "CONSTRAINT [PK_t] PRIMARY KEY ([id])") {
		t.Errorf("definition: %s %v", def, err)
	}
	comp, err := CompletionSchema(ctx, pool)
	if err != nil || len(comp["dbo.t"]) != 8 {
		t.Errorf("completion: %v %v", comp, err)
	}
	dbs, err := Databases(ctx, pool)
	if err != nil || len(dbs) < 2 {
		t.Errorf("databases: %v %v", dbs, err)
	}
}
