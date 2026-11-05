package mongo

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/thennavanr/sql-studio/internal/engine"
	"github.com/thennavanr/sql-studio/internal/store"
)

func TestNormalizeURI(t *testing.T) {
	c, err := Normalize(store.Connection{Kind: store.KindMongoDB, URI: "mongodb+srv://ann%40corp:p%40ss%3Aw0rd!@cluster0.abc.mongodb.net/shop?retryWrites=true&w=majority"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "p@ss:w0rd!" {
		t.Errorf("password = %q", c.Password)
	}
	if strings.Contains(c.URI, "w0rd") || c.URI != "mongodb+srv://ann%40corp@cluster0.abc.mongodb.net/shop?retryWrites=true&w=majority" {
		t.Errorf("stored URI must not contain the password: %s", c.URI)
	}
	if c.User != "ann@corp" || c.Server != "cluster0.abc.mongodb.net" || c.Database != "shop" || c.Name != "cluster0.abc.mongodb.net / shop" {
		t.Errorf("profile = %+v", c)
	}
	if !NeedsPassword(c) {
		t.Error("user in URI should need a password")
	}
	// Re-injecting the password reproduces a valid URI.
	u, _ := parseURI(c.URI)
	u.password = c.Password
	if got := u.build(true); got != "mongodb+srv://ann%40corp:p%40ss%3Aw0rd%21@cluster0.abc.mongodb.net/shop?retryWrites=true&w=majority" {
		t.Errorf("rebuilt = %s", got)
	}
	c2, _ := Normalize(store.Connection{Kind: store.KindMongoDB, URI: "mongodb://h1:27017,h2:27018/?replicaSet=rs0"})
	if c2.Server != "h1:27017,h2:27018" || c2.Database != "" || NeedsPassword(c2) {
		t.Errorf("multi-host: %+v", c2)
	}
	x509, _ := Normalize(store.Connection{Kind: store.KindMongoDB, URI: "mongodb://CN=me@h/?authMechanism=MONGODB-X509&tls=true"})
	if NeedsPassword(x509) {
		t.Error("x509 does not use a password")
	}
	for _, bad := range []string{"", "http://x", "mongodb://", "mongodb://user@/db"} {
		if _, err := Normalize(store.Connection{URI: bad}); err == nil {
			t.Errorf("Normalize(%q) should fail", bad)
		}
	}
}

// ---- integration (requires SQLSTUDIO_TEST_MONGO_URI) ----

type sink struct {
	mu   sync.Mutex
	cols map[int][]engine.Column
	rows map[int][][]any
	docs map[int]DocsInfo
	msgs []engine.Message
}

func newSink() *sink {
	return &sink{cols: map[int][]engine.Column{}, rows: map[int][][]any{}, docs: map[int]DocsInfo{}}
}
func (s *sink) ResultSet(i int, c []engine.Column) { s.mu.Lock(); s.cols[i] = c; s.mu.Unlock() }
func (s *sink) Rows(i int, r [][]any) {
	s.mu.Lock()
	s.rows[i] = append(s.rows[i], r...)
	s.mu.Unlock()
}
func (s *sink) ResultDone(int, int, bool) {}
func (s *sink) Message(m engine.Message)  { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }
func (s *sink) Docs(i int, d DocsInfo)    { s.mu.Lock(); s.docs[i] = d; s.mu.Unlock() }
func (s *sink) text() string {
	var b strings.Builder
	for _, m := range s.msgs {
		b.WriteString(m.Kind + ": " + m.Text + "\n")
	}
	return b.String()
}

func testProfile(t *testing.T) store.Connection {
	uri := os.Getenv("SQLSTUDIO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set SQLSTUDIO_TEST_MONGO_URI (e.g. mongodb://admin:pass@localhost:27017/?authSource=admin) to run MongoDB integration tests")
	}
	c, err := Normalize(store.Connection{ID: "m", Kind: store.KindMongoDB, URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIntegrationMongo(t *testing.T) {
	c := testProfile(t)
	ctx := context.Background()
	if _, err := Test(ctx, c); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	cl, err := m.Connect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	info := Info(ctx, cl, c)
	if info.Version == "" {
		t.Errorf("no version: %+v", info)
	}
	const db = "sqlstudio_test"
	_ = cl.Database(db).Drop(ctx)

	run := func(script string, opts ...engine.ExecOptions) (*sink, engine.Summary) {
		t.Helper()
		s := newSink()
		o := engine.ExecOptions{MaxRows: 100000}
		if len(opts) > 0 {
			o = opts[0]
		}
		sum := Execute(ctx, cl, db, script, o, s)
		return s, sum
	}

	s, sum := run(`db.orders.insertMany([
	  { _id: 1, item: "apple",  qty: 5,  price: Decimal128("1.25"), tags: ["fruit", "red"], when: ISODate("2024-01-02T03:04:05Z") },
	  { _id: 2, item: "banana", qty: 20, price: Decimal128("0.50"), tags: ["fruit"], n: Long("9007199254740993") },
	  { _id: 3, item: "carrot", qty: 12, price: Decimal128("0.75"), tags: ["veg"], meta: { origin: "NL", organic: true } }
	])
	db.orders.createIndex({ item: 1 }, { unique: true })`)
	if sum.Errors != 0 {
		t.Fatalf("insert failed: %s", s.text())
	}
	if !strings.Contains(s.text(), "insertedCount: 3") {
		t.Errorf("insert message: %s", s.text())
	}

	// find + sort + limit, projection disables editing
	s, sum = run(`db.orders.find({ qty: { $gt: 6 } }).sort({ qty: -1 }).limit(5)
db.orders.find({}, { item: 1 })`)
	if sum.Errors != 0 || sum.ResultSets != 2 {
		t.Fatalf("find: %+v %s", sum, s.text())
	}
	if len(s.rows[0]) != 2 || s.rows[0][0][1] != "banana" || !s.docs[0].Editable || s.docs[0].Truncated {
		t.Errorf("find result: rows=%v docs=%+v", s.rows[0], s.docs[0])
	}
	if s.docs[1].Editable {
		t.Error("projected results must not be editable")
	}
	if s.docs[0].IDs[0] != "2" || !strings.Contains(s.docs[0].Docs[0], "n: Long('9007199254740993')") {
		t.Errorf("ids/docs: %v %s", s.docs[0].IDs, s.docs[0].Docs[0])
	}
	colNames := []string{}
	for _, col := range s.cols[0] {
		colNames = append(colNames, col.Name)
	}
	if strings.Join(colNames, ",") != "_id,item,qty,price,tags,n,meta" {
		t.Errorf("columns = %v", colNames)
	}
	if !s.cols[0][2].Numeric || s.cols[0][1].Numeric {
		t.Errorf("numeric flags: %+v", s.cols[0])
	}
	if !strings.Contains(s.docs[0].JSON[0], `"$numberDecimal":"0.50"`) {
		t.Errorf("ext json: %s", s.docs[0].JSON[0])
	}

	// aggregate, counts, distinct, indexes, show, use, runCommand
	s, sum = run(`db.orders.aggregate([{ $unwind: "$tags" }, { $group: { _id: "$tags", total: { $sum: "$qty" } } }, { $sort: { _id: 1 } }])
db.orders.countDocuments({ tags: "fruit" })
db.orders.distinct("tags")
db.orders.getIndexes()
show collections
db.runCommand({ ping: 1 })`)
	if sum.Errors != 0 || sum.ResultSets != 6 {
		t.Fatalf("misc: %+v %s", sum, s.text())
	}
	if len(s.rows[0]) != 3 || s.rows[0][0][0] != "fruit" || s.rows[0][0][1] != "25" {
		t.Errorf("aggregate rows: %v", s.rows[0])
	}
	if s.rows[1][0][0] != "2" {
		t.Errorf("count: %v", s.rows[1])
	}
	if len(s.rows[2]) != 3 {
		t.Errorf("distinct: %v", s.rows[2])
	}
	if len(s.rows[3]) != 2 {
		t.Errorf("indexes: %v", s.rows[3])
	}
	if len(s.rows[4]) != 1 || s.rows[4][0][0] != "orders" {
		t.Errorf("show collections: %v", s.rows[4])
	}

	// updates and deletes
	s, sum = run(`db.orders.updateOne({ _id: 1 }, { $inc: { qty: 1 } })
db.orders.updateMany({ tags: "fruit" }, { $set: { fresh: true } })
db.orders.deleteOne({ _id: 3 })
db.orders.countDocuments()`)
	if sum.Errors != 0 {
		t.Fatalf("writes: %s", s.text())
	}
	for _, w := range []string{"matchedCount: 1, modifiedCount: 1", "matchedCount: 2, modifiedCount: 2", "deletedCount: 1"} {
		if !strings.Contains(s.text(), w) {
			t.Errorf("missing %q in %s", w, s.text())
		}
	}
	if s.rows[0][0][0] != "2" {
		t.Errorf("count after delete: %v", s.rows)
	}

	// Errors stop the script, with the statement's line number.
	s, sum = run("db.orders.countDocuments()\ndb.orders.insertOne({ _id: 1 })\ndb.orders.deleteMany({})")
	if sum.Errors != 1 || sum.Batches != 2 {
		t.Errorf("error should stop the script: %+v %s", sum, s.text())
	}
	var errMsg engine.Message
	for _, m := range s.msgs {
		if m.Kind == "error" {
			errMsg = m
		}
	}
	if errMsg.Line != 2 || !strings.Contains(errMsg.Text, "duplicate key") {
		t.Errorf("error message: %+v", errMsg)
	}
	if n, _ := cl.Database(db).Collection("orders").CountDocuments(ctx, bson.D{}); n != 2 {
		t.Errorf("deleteMany after the error must not run; count = %d", n)
	}

	// Syntax errors report a line.
	s, sum = run("db.orders.find({ qty: })")
	if sum.Errors != 1 || s.msgs[0].Line != 1 || !strings.Contains(s.msgs[0].Text, "Syntax error") {
		t.Errorf("syntax error: %+v", s.msgs)
	}

	// use switches the database for later statements and is reported back.
	s, sum = run("use other_db\ndb.getName()")
	if sum.Database != "other_db" || s.rows[0][0][0] != "other_db" {
		t.Errorf("use: %+v %v", sum, s.rows)
	}

	// Default limit with truncation notice.
	docs := make([]any, 1100)
	for i := range docs {
		docs[i] = bson.D{{Key: "i", Value: int32(i)}}
	}
	if _, err := cl.Database(db).Collection("many").InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	s, _ = run("db.many.find()")
	if len(s.rows[0]) != DefaultFindLimit || !s.docs[0].Truncated || !strings.Contains(s.text(), "use .limit() and .skip()") {
		t.Errorf("default limit: %d rows truncated=%v", len(s.rows[0]), s.docs[0].Truncated)
	}
	s, _ = run("db.many.find().skip(1090).limit(20)")
	if len(s.rows[0]) != 10 || s.docs[0].Truncated {
		t.Errorf("skip/limit: %d rows truncated=%v", len(s.rows[0]), s.docs[0].Truncated)
	}

	// Edit round trip through the Documents view preserves types exactly.
	coll := cl.Database(db).Collection("orders")
	before, _ := coll.FindOne(ctx, bson.D{{Key: "_id", Value: int32(2)}}).Raw()
	text := FormatDoc(before, true)
	edited := strings.Replace(text, "item: 'banana'", "item: 'plantain'", 1)
	saved, err := ReplaceDocument(ctx, cl, db, "orders", "2", edited)
	if err != nil {
		t.Fatal(err)
	}
	if saved != edited {
		t.Errorf("saved document differs from the edit:\n%s\n---\n%s", saved, edited)
	}
	if _, err := ReplaceDocument(ctx, cl, db, "orders", "2", strings.Replace(text, "_id: 2", "_id: 99", 1)); err == nil || !strings.Contains(err.Error(), "_id cannot be changed") {
		t.Errorf("changing _id should be refused: %v", err)
	}
	id, err := InsertDocument(ctx, cl, db, "orders", "{ item: 'kiwi', qty: NumberInt(3) }")
	if err != nil || !strings.HasPrefix(id, "ObjectId('") {
		t.Fatalf("insert: %q %v", id, err)
	}
	if err := DeleteDocument(ctx, cl, db, "orders", id); err != nil {
		t.Fatal(err)
	}
	if err := DeleteDocument(ctx, cl, db, "orders", id); err == nil {
		t.Error("deleting twice should report not found")
	}

	// Explorer helpers.
	objs, err := Collections(ctx, cl, db)
	if err != nil || len(objs) != 2 || objs[0].Name != "many" || objs[0].Kind != "collection" {
		t.Errorf("collections: %+v %v", objs, err)
	}
	dbs, err := Databases(ctx, cl, c)
	if err != nil || !contains(dbs, db) || isSystemDB(dbs[0]) {
		t.Errorf("databases: %v %v", dbs, err)
	}
	fields, err := SampleFields(ctx, cl, db, "orders")
	if err != nil || !contains(fields, "qty") {
		t.Errorf("fields: %v %v", fields, err)
	}
	_ = cl.Database(db).Drop(ctx)
	_ = cl.Database("other_db").Drop(ctx)
}
