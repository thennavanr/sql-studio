package redisdb

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/thennavanr/sql-studio/internal/engine"
	"github.com/thennavanr/sql-studio/internal/store"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		want Target
	}{
		{"redis://localhost", Target{Host: "localhost", Port: 6379}},
		{"rediss://:s3cr3t@cache.example.com:6390/2", Target{TLS: true, Password: "s3cr3t", Host: "cache.example.com", Port: 6390, DB: 2}},
		{"redis://ann:p%40ss@h:1/0", Target{User: "ann", Password: "p@ss", Host: "h", Port: 1}},
		{"myapp.redis.cache.windows.net:6380,password=Key+abc=,ssl=True,abortConnect=False", Target{TLS: true, Password: "Key+abc=", Host: "myapp.redis.cache.windows.net", Port: 6380}},
		{"myapp.redis.cache.windows.net", Target{TLS: true, Host: "myapp.redis.cache.windows.net", Port: 6380}},
		{"10.0.0.5:7000", Target{Host: "10.0.0.5", Port: 7000}},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseTarget(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "http://x", "redis://", "redis://h:abc", "redis://h/x"} {
		if _, err := ParseTarget(bad); err == nil {
			t.Errorf("ParseTarget(%q) should fail", bad)
		}
	}
	c, err := Normalize(store.Connection{Kind: store.KindRedis, URI: "rediss://:topsecret@cache.example.com:6380/3"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "topsecret" || strings.Contains(c.URI, "topsecret") || c.URI != "rediss://cache.example.com:6380/3" || c.Database != "db3" || c.Server != "cache.example.com:6380" {
		t.Errorf("Normalize: %+v", c)
	}
}

func TestParseScript(t *testing.T) {
	cmds, err := ParseScript("# comment\nSET greeting \"hello world\\n\"\n\n  // another\nHSET user:1 name 'O\\'Brien' age 42\nGET  \"\\x41\\x42\"")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 {
		t.Fatalf("got %d commands", len(cmds))
	}
	if strings.Join(cmds[0].Args, "|") != "SET|greeting|hello world\n" || cmds[0].Line != 2 {
		t.Errorf("cmd0 = %q line %d", cmds[0].Args, cmds[0].Line)
	}
	if strings.Join(cmds[1].Args, "|") != "HSET|user:1|name|O'Brien|age|42" || cmds[1].Line != 5 {
		t.Errorf("cmd1 = %q", cmds[1].Args)
	}
	if cmds[2].Args[1] != "AB" {
		t.Errorf("hex escape: %q", cmds[2].Args[1])
	}
	for _, bad := range []string{`GET "open`, `GET 'open`, `GET "a"b`} {
		if _, err := ParseScript("PING\n" + bad); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("ParseScript(%q) error = %v", bad, err)
		}
	}
	if q := QuoteArg("a b\"c\x01"); q != `"a b\"c\x01"` {
		t.Errorf("QuoteArg = %s", q)
	}
}

type sink struct {
	mu   sync.Mutex
	cols map[int][]engine.Column
	rows map[int][][]any
	msgs []engine.Message
}

func newSink() *sink                               { return &sink{cols: map[int][]engine.Column{}, rows: map[int][][]any{}} }
func (s *sink) ResultSet(i int, c []engine.Column) { s.mu.Lock(); s.cols[i] = c; s.mu.Unlock() }
func (s *sink) Rows(i int, r [][]any) {
	s.mu.Lock()
	s.rows[i] = append(s.rows[i], r...)
	s.mu.Unlock()
}
func (s *sink) ResultDone(int, int, bool) {}
func (s *sink) Message(m engine.Message)  { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }
func (s *sink) text() string {
	var b strings.Builder
	for _, m := range s.msgs {
		b.WriteString(m.Kind + ": " + m.Text + "\n")
	}
	return b.String()
}
func colNames(c []engine.Column) string {
	var n []string
	for _, x := range c {
		n = append(n, x.Name)
	}
	return strings.Join(n, ",")
}

func TestIntegrationRedis(t *testing.T) {
	uri := os.Getenv("SQLSTUDIO_TEST_REDIS_URI")
	if uri == "" {
		t.Skip("set SQLSTUDIO_TEST_REDIS_URI (e.g. redis://:pass@localhost:6379/0) to run Redis integration tests")
	}
	ctx := context.Background()
	c, err := Normalize(store.Connection{ID: "r", Kind: store.KindRedis, URI: uri, Database: "db9"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Test(ctx, c); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	cl, err := m.Connect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if info := Info(ctx, cl, c); info.Version == "" {
		t.Errorf("info: %+v", info)
	}
	cl.FlushDB(ctx)
	clients := func(ctx context.Context, db int) (*redis.Client, error) { return m.Client(ctx, c.ID, db) }
	run := func(script string) (*sink, engine.Summary) {
		s := newSink()
		return s, Execute(ctx, Clients(clients), 9, script, engine.ExecOptions{}, s)
	}

	s, sum := run(`# seed
SET greeting "hello world"
HSET user:1 name Ann age 42 "full name" "Ann Lee"
RPUSH queue a b c
SADD tags red green
ZADD scores 10 ann 20 bob
XADD events 1-1 type login user ann
SET bin "\x00\xff"`)
	if sum.Errors != 0 {
		t.Fatalf("seed: %s", s.text())
	}

	s, sum = run(`GET greeting
HGETALL user:1
LRANGE queue 0 -1
ZRANGE scores 0 -1 WITHSCORES
SCAN 0 MATCH user:* COUNT 100
XRANGE events - +
GET missing
INCR counter
GET bin`)
	if sum.Errors != 0 || sum.ResultSets != 9 {
		t.Fatalf("reads: %+v\n%s", sum, s.text())
	}
	if s.rows[0][0][0] != "hello world" {
		t.Errorf("GET: %v", s.rows[0])
	}
	if colNames(s.cols[1]) != "field,value" || len(s.rows[1]) != 3 || s.rows[1][0][0] != "age" || s.rows[1][0][1] != "42" {
		t.Errorf("HGETALL: %s %v", colNames(s.cols[1]), s.rows[1])
	}
	if colNames(s.cols[2]) != "#,value" || len(s.rows[2]) != 3 || s.rows[2][2][1] != "c" {
		t.Errorf("LRANGE: %v", s.rows[2])
	}
	if colNames(s.cols[3]) != "#,member,score" || s.rows[3][1][1] != "bob" || s.rows[3][1][2] != "20" {
		t.Errorf("ZRANGE WITHSCORES: %s %v", colNames(s.cols[3]), s.rows[3])
	}
	if len(s.rows[4]) != 1 || s.rows[4][0][1] != "user:1" || !strings.Contains(s.text(), "next cursor 0") {
		t.Errorf("SCAN: %v\n%s", s.rows[4], s.text())
	}
	if colNames(s.cols[5]) != "#,id,fields" || s.rows[5][0][1] != "1-1" || s.rows[5][0][2] != "type=login  user=ann" {
		t.Errorf("XRANGE: %s %v", colNames(s.cols[5]), s.rows[5])
	}
	if s.rows[6][0][0] != nil || !strings.Contains(s.text(), "(nil)") {
		t.Errorf("nil reply: %v", s.rows[6])
	}
	if s.rows[7][0][0] != "1" || !s.cols[7][0].Numeric {
		t.Errorf("INCR: %v", s.rows[7])
	}
	if s.rows[8][0][0] != `\x00\xff` {
		t.Errorf("binary: %q", s.rows[8][0][0])
	}

	// errors stop the script and carry the line
	s, sum = run("SET a 1\nHGETALL greeting\nSET b 2")
	if sum.Errors != 1 || sum.Batches != 2 || !strings.Contains(s.text(), "Line 2: WRONGTYPE") {
		t.Errorf("error handling: %+v\n%s", sum, s.text())
	}
	if n, _ := cl.Exists(ctx, "b").Result(); n != 0 {
		t.Error("command after the error ran")
	}

	// SELECT switches database for later commands and is reported back
	s, sum = run("SELECT 10\nSET only-in-10 x\nDBSIZE")
	if sum.Database != "db10" || s.rows[0][0][0] != "1" {
		t.Errorf("SELECT: %+v %v\n%s", sum, s.rows, s.text())
	}
	cl10, _ := m.Client(ctx, c.ID, 10)
	defer cl10.FlushDB(ctx)
	if n, _ := cl.Exists(ctx, "only-in-10").Result(); n != 0 {
		t.Error("SELECT leaked into db9")
	}

	// blocked commands
	s, sum = run("SUBSCRIBE news")
	if sum.Errors != 1 || !strings.Contains(s.text(), "not supported") {
		t.Errorf("SUBSCRIBE should be blocked: %s", s.text())
	}

	// explorer
	keys, err := Keys(ctx, cl, "")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, k := range keys {
		kinds[k.Name] = k.Kind
	}
	want := map[string]string{"greeting": "string", "user:1": "hash", "queue": "list", "tags": "set", "scores": "zset", "events": "stream"}
	for k, v := range want {
		if kinds[k] != v {
			t.Errorf("Keys: %s is %q, want %q (%v)", k, kinds[k], v, kinds)
		}
	}
	dbs, err := Databases(ctx, cl, c)
	if err != nil || !strings.Contains(strings.Join(dbs, ","), "db9") || !strings.Contains(strings.Join(dbs, ","), "db10") {
		t.Errorf("Databases: %v %v", dbs, err)
	}

	// validation
	msgs := Validate(ctx, cl, "GET a\nGETT a\nSET onlykey\nKEYS *\nHGET \"unterminated")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "line 5") {
		t.Errorf("syntax error first: %+v", msgs)
	}
	msgs = Validate(ctx, cl, "GET a\nGETT a\nSET onlykey\nKEYS *")
	var all strings.Builder
	for _, m := range msgs {
		all.WriteString(m.Kind + ": " + m.Text + "\n")
	}
	for _, w := range []string{"Line 2: unknown command 'GETT'", "Line 3: wrong number of arguments for 'set' (expects at least 2)", "KEYS scans the whole database"} {
		if !strings.Contains(all.String(), w) {
			t.Errorf("Validate missing %q:\n%s", w, all.String())
		}
	}
	msgs = Validate(ctx, cl, "GET a\nHSET h f v")
	if !strings.Contains(msgs[len(msgs)-1].Text, "Script is valid") {
		t.Errorf("valid script: %+v", msgs)
	}
	cl.FlushDB(ctx)
}
