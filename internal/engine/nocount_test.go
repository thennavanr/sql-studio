package engine

import (
	"context"
	"testing"
)

// Session state such as SET NOCOUNT ON persists between executions in a tab
// and must not affect result delivery.
func TestIntegrationNoCount(t *testing.T) {
	c := testConn(t)
	ctx := context.Background()
	m := NewManager()
	defer m.Close()
	m.Register(c)
	s, err := m.Session(ctx, "nc", c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SET NOCOUNT ON; SELECT 1 AS a", "SELECT 1 AS a; SELECT 1 AS b", "SET NOCOUNT OFF; SELECT 1 AS a"} {
		sink := newSink()
		sum := s.Execute(ctx, q, ExecOptions{}, sink)
		if sum.Errors != 0 || len(sink.rows) != sum.ResultSets || sum.ResultSets == 0 {
			t.Fatalf("%q: sets=%d rows=%v msgs=%s", q, sum.ResultSets, sink.rows, sink.text())
		}
		for i, r := range sink.rows {
			if len(r) != 1 || r[0][0] != "1" {
				t.Errorf("%q result %d: %v", q, i, r)
			}
		}
	}
}
