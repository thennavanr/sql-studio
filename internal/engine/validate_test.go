package engine

import (
	"context"
	"strings"
	"testing"
)

func TestIntegrationValidate(t *testing.T) {
	c := testConn(t)
	c.Database = "sqlstudio_test"
	ctx := context.Background()
	m := NewManager()
	defer m.Close()
	m.Register(c)
	pool, err := m.Pool(ctx, c.ID, "sqlstudio_test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, "IF OBJECT_ID('dbo.vt') IS NULL CREATE TABLE dbo.vt (id int, name nvarchar(20))"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		script  string
		wantOK  bool
		want    []string // substrings expected in the messages
		wantLin int      // expected line of the first error, 0 = don't care
	}{
		{"valid", "SELECT id, name FROM dbo.vt WHERE id = 1;\nGO\nSELECT COUNT(*) FROM dbo.vt", true, []string{"Query is valid", "2 batches"}, 0},
		{"syntax error in 2nd batch", "SELECT 1\nGO\nSELECT 2\nSELEC * FROM dbo.vt", false, []string{"Msg 102", "Incorrect syntax"}, 4},
		{"wrong column", "SELECT nocol FROM dbo.vt", false, []string{"Msg 207", "Invalid column name 'nocol'"}, 1},
		{"missing table", "SELECT 1;\n\nSELECT * FROM dbo.does_not_exist", false, []string{"Msg 208", "does_not_exist"}, 3},
		{"missing proc", "EXEC dbo.no_such_proc", false, []string{"Msg 2812"}, 1},
		{"created earlier in script", "CREATE TABLE dbo.made_here (a int)\nGO\nSELECT a FROM dbo.made_here", true, []string{"Query is valid"}, 0},
		{"temp table", "CREATE TABLE #tmp (a int);\nSELECT a FROM #tmp;", true, []string{"Query is valid"}, 0},
		{"writes are not executed", "INSERT dbo.vt (id, name) VALUES (99, N'should-not-exist');\nUPDATE dbo.vt SET name = N'x';\nDELETE FROM dbo.vt;", true, []string{"Nothing was executed"}, 0},
	}
	for _, tc := range cases {
		msgs, err := Validate(ctx, pool, tc.script)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var all strings.Builder
		ok := true
		firstLine := 0
		for _, m := range msgs {
			all.WriteString(m.Kind + ": " + m.Text + "\n")
			if m.Kind == "error" {
				if ok {
					firstLine = m.Line
				}
				ok = false
			}
		}
		if ok != tc.wantOK {
			t.Errorf("%s: ok=%v, messages:\n%s", tc.name, ok, all.String())
		}
		for _, w := range tc.want {
			if !strings.Contains(all.String(), w) {
				t.Errorf("%s: missing %q in:\n%s", tc.name, w, all.String())
			}
		}
		if tc.wantLin != 0 && firstLine != tc.wantLin {
			t.Errorf("%s: first error on line %d, want %d", tc.name, firstLine, tc.wantLin)
		}
	}

	// Nothing ran: no row inserted, table not created.
	var n int
	if err := pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM dbo.vt WHERE id = 99").Scan(&n); err != nil || n != 0 {
		t.Errorf("validation executed an INSERT (count=%d, err=%v)", n, err)
	}
	var obj any
	_ = pool.QueryRowContext(ctx, "SELECT OBJECT_ID('dbo.made_here')").Scan(&obj)
	if obj != nil {
		t.Error("validation created a table")
	}
	// NOEXEC / SHOWPLAN must not leak into pooled connections.
	for i := 0; i < 5; i++ {
		var one int
		if err := pool.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("pooled connection affected by validation: %v", err)
		}
	}
}
