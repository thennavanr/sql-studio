package mongo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/thennavanr/sql-studio/internal/engine"
)

// DefaultFindLimit is applied to find()/aggregate() without an explicit limit.
const DefaultFindLimit = 1000

// DocsInfo accompanies a result set produced by a MongoDB command.
type DocsInfo struct {
	Docs       []string `json:"docs"`       // pretty shell syntax, one per result
	JSON       []string `json:"json"`       // relaxed Extended JSON, for export
	IDs        []string `json:"ids"`        // _id in shell syntax ("" if none)
	Collection string   `json:"collection"` // source collection, if any
	Database   string   `json:"database"`
	Editable   bool     `json:"editable"` // documents may be edited/deleted in place
	Truncated  bool     `json:"truncated"`
	Limit      int      `json:"limit"`
}

// Sink receives streaming output; it extends the SQL sink with document payloads.
type Sink interface {
	engine.Sink
	Docs(index int, info DocsInfo)
}

// Execute runs a script of shell commands against database db. Execution
// stops at the first error so a failed step never runs later writes.
func Execute(parent context.Context, cl *mongo.Client, db, script string, opts engine.ExecOptions, sink Sink) engine.Summary {
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

	ex := &executor{cl: cl, db: db, sink: sink, opts: opts}
	sum := engine.Summary{Database: db}
	stmts, err := ParseScript(script)
	if err != nil {
		var se *SyntaxError
		line := 0
		if errors.As(err, &se) {
			line = se.Line
		}
		sink.Message(engine.Message{Kind: "error", Text: "Syntax error: " + err.Error(), Line: line, Time: nowMs()})
		sum.Errors = 1
		sum.ElapsedMs = time.Since(start).Milliseconds()
		return sum
	}
	for _, st := range stmts {
		if ctx.Err() != nil {
			break
		}
		sum.Batches++
		if err := ex.run(ctx, st); err != nil {
			if ctx.Err() != nil {
				break
			}
			sum.Errors++
			sink.Message(engine.Message{Kind: "error", Text: friendlyError(err).Error(), Line: st.Line, Time: nowMs()})
			break
		}
	}
	if ctx.Err() != nil {
		sum.Cancelled = true
		text := "Command was cancelled."
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			text = "Command timeout expired."
		}
		sink.Message(engine.Message{Kind: "error", Text: text, Time: nowMs()})
	} else if sum.Errors == 0 && !ex.anyOutput {
		sink.Message(engine.Message{Kind: "info", Text: "Commands completed successfully.", Time: nowMs()})
	}
	sum.ResultSets = ex.results
	sum.Database = ex.db
	sum.ElapsedMs = time.Since(start).Milliseconds()
	return sum
}

func nowMs() int64 { return time.Now().UnixMilli() }

type executor struct {
	cl        *mongo.Client
	db        string
	sink      Sink
	opts      engine.ExecOptions
	results   int
	anyOutput bool
}

func (e *executor) info(text string) {
	e.anyOutput = true
	e.sink.Message(engine.Message{Kind: "info", Text: text, Time: nowMs()})
}

func (e *executor) maxDocs() int {
	if e.opts.MaxRows > 0 {
		return e.opts.MaxRows
	}
	return 100000
}

// ---- argument helpers ----

func argDoc(c Call, i int, what string) (bson.D, error) {
	if i >= len(c.Args) || c.Args[i] == nil {
		return bson.D{}, nil
	}
	d, ok := c.Args[i].(bson.D)
	if !ok {
		return nil, fmt.Errorf("%s(): %s must be a document like { field: value }", c.Name, what)
	}
	return d, nil
}

func argInt(c Call, i int) (int64, error) {
	if i >= len(c.Args) {
		return 0, fmt.Errorf("%s() needs a number", c.Name)
	}
	n, ok := asInt64(c.Args[i])
	if !ok {
		return 0, fmt.Errorf("%s() needs a whole number", c.Name)
	}
	return n, nil
}

func argString(c Call, i int, what string) (string, error) {
	if i < len(c.Args) {
		if s, ok := c.Args[i].(string); ok {
			return s, nil
		}
	}
	return "", fmt.Errorf("%s(): %s must be a string", c.Name, what)
}

func lookup(d bson.D, key string) (any, bool) {
	for _, el := range d {
		if el.Key == key {
			return el.Value, true
		}
	}
	return nil, false
}

func toRaw(v any) (bson.RawValue, error) {
	b, err := bson.Marshal(bson.D{{Key: "v", Value: v}})
	if err != nil {
		return bson.RawValue{}, err
	}
	return bson.Raw(b).Lookup("v"), nil
}

// ---- dispatch ----

func (e *executor) run(ctx context.Context, st Statement) error {
	switch st.Kind {
	case "use":
		e.db = st.Arg
		e.info("switched to db " + st.Arg)
		return nil
	case "show":
		if st.Arg == "dbs" {
			return e.showDBs(ctx)
		}
		return e.collectionNames(ctx)
	case "db":
		return e.dbMethod(ctx, st.Chain)
	}
	return e.collMethod(ctx, st.Collection, st.Chain)
}

func (e *executor) showDBs(ctx context.Context) error {
	res, err := listDatabases(ctx, e.cl)
	if err != nil {
		return err
	}
	var vals []bson.RawValue
	for _, d := range res.Databases {
		v, _ := toRaw(bson.D{{Key: "name", Value: d.Name}, {Key: "sizeOnDisk", Value: d.SizeOnDisk}, {Key: "empty", Value: d.Empty}})
		vals = append(vals, v)
	}
	e.emit(vals, DocsInfo{})
	return nil
}

func (e *executor) collectionNames(ctx context.Context) error {
	names, err := listCollectionNames(ctx, e.cl.Database(e.db))
	if err != nil {
		return err
	}
	sort.Strings(names)
	var vals []bson.RawValue
	for _, n := range names {
		v, _ := toRaw(n)
		vals = append(vals, v)
	}
	e.emit(vals, DocsInfo{})
	return nil
}

func (e *executor) runCommand(ctx context.Context, db string, cmd bson.D) error {
	raw, err := e.cl.Database(db).RunCommand(ctx, cmd).Raw()
	if err != nil {
		return err
	}
	e.emitDoc(raw)
	return nil
}

func (e *executor) dbMethod(ctx context.Context, chain []Call) error {
	c := chain[0]
	if len(chain) > 1 {
		return fmt.Errorf("db.%s() cannot be followed by .%s()", c.Name, chain[1].Name)
	}
	switch c.Name {
	case "getCollectionNames":
		return e.collectionNames(ctx)
	case "getCollectionInfos":
		filter, err := argDoc(c, 0, "filter")
		if err != nil {
			return err
		}
		cur, err := e.cl.Database(e.db).ListCollections(ctx, filter)
		if err != nil {
			return err
		}
		return e.drainCursor(ctx, cur, DocsInfo{}, 0)
	case "stats":
		return e.runCommand(ctx, e.db, bson.D{{Key: "dbStats", Value: 1}})
	case "runCommand":
		cmd, err := argDoc(c, 0, "command")
		if err != nil || len(cmd) == 0 {
			return errors.New("usage: db.runCommand({ ping: 1 })")
		}
		return e.runCommand(ctx, e.db, cmd)
	case "adminCommand":
		cmd, err := argDoc(c, 0, "command")
		if err != nil || len(cmd) == 0 {
			return errors.New("usage: db.adminCommand({ listDatabases: 1 })")
		}
		return e.runCommand(ctx, "admin", cmd)
	case "getName":
		return e.emitScalar(e.db)
	case "version":
		var bi struct {
			Version string `bson:"version"`
		}
		if err := e.cl.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&bi); err != nil {
			return err
		}
		return e.emitScalar(bi.Version)
	case "createCollection":
		name, err := argString(c, 0, "name")
		if err != nil {
			return err
		}
		if err := e.cl.Database(e.db).CreateCollection(ctx, name); err != nil {
			return err
		}
		e.info(fmt.Sprintf("{ ok: 1 }  created collection %s", name))
		return nil
	}
	return fmt.Errorf("db.%s() is not supported. Supported: getCollectionNames, getCollectionInfos, stats, runCommand, adminCommand, createCollection, getName, version", c.Name)
}

func (e *executor) collMethod(ctx context.Context, name string, chain []Call) error {
	coll := e.cl.Database(e.db).Collection(name)
	c := chain[0]
	mods := chain[1:]
	modsAllowed := map[string]bool{"find": true, "aggregate": true}
	if !modsAllowed[c.Name] {
		for _, m := range mods {
			if m.Name != "pretty" && m.Name != "toArray" {
				return fmt.Errorf("%s() cannot be followed by .%s()", c.Name, m.Name)
			}
		}
	}
	info := DocsInfo{Collection: name, Database: e.db}

	switch c.Name {
	case "find":
		return e.find(ctx, coll, c, mods, info)
	case "findOne":
		filter, err := argDoc(c, 0, "filter")
		if err != nil {
			return err
		}
		proj, err := argDoc(c, 1, "projection")
		if err != nil {
			return err
		}
		o := options.FindOne()
		if len(proj) > 0 {
			o.SetProjection(proj)
		}
		raw, err := coll.FindOne(ctx, filter, o).Raw()
		if errors.Is(err, mongo.ErrNoDocuments) {
			return e.emitScalar(nil)
		}
		if err != nil {
			return err
		}
		v, _ := toRaw(raw)
		info.Editable = len(proj) == 0
		e.emit([]bson.RawValue{v}, info)
		return nil
	case "aggregate":
		var pipeline bson.A
		if len(c.Args) > 0 {
			if a, ok := c.Args[0].(bson.A); ok {
				pipeline = a
			} else {
				for _, a := range c.Args { // aggregate(stage1, stage2, ...)
					if _, ok := a.(bson.D); !ok {
						return errors.New("aggregate() takes an array of stages, e.g. aggregate([{ $match: { ... } }])")
					}
					pipeline = append(pipeline, a)
				}
			}
		}
		if pipeline == nil {
			pipeline = bson.A{}
		}
		o := options.Aggregate()
		if len(c.Args) > 1 {
			if od, ok := c.Args[1].(bson.D); ok {
				if v, ok := lookup(od, "allowDiskUse"); ok {
					b, _ := v.(bool)
					o.SetAllowDiskUse(b)
				}
			}
		}
		cur, err := coll.Aggregate(ctx, pipeline, o)
		if err != nil {
			return err
		}
		return e.drainCursor(ctx, cur, info, DefaultFindLimit)
	case "countDocuments", "count":
		filter, err := argDoc(c, 0, "filter")
		if err != nil {
			return err
		}
		n, err := coll.CountDocuments(ctx, filter)
		if err != nil {
			return err
		}
		return e.emitScalar(intValue(n))
	case "estimatedDocumentCount":
		n, err := coll.EstimatedDocumentCount(ctx)
		if err != nil {
			return err
		}
		return e.emitScalar(intValue(n))
	case "distinct":
		field, err := argString(c, 0, "field")
		if err != nil {
			return err
		}
		filter, err := argDoc(c, 1, "filter")
		if err != nil {
			return err
		}
		res := coll.Distinct(ctx, field, filter)
		if err := res.Err(); err != nil {
			return err
		}
		raw, err := res.Raw()
		if err != nil {
			return err
		}
		vals, err := raw.Values()
		if err != nil {
			return err
		}
		e.emit(vals, DocsInfo{})
		return nil
	case "getIndexes":
		cur, err := coll.Indexes().List(ctx)
		if err != nil {
			return err
		}
		return e.drainCursor(ctx, cur, DocsInfo{}, 0)
	case "createIndex":
		keys, err := argDoc(c, 0, "keys")
		if err != nil || len(keys) == 0 {
			return errors.New("usage: createIndex({ field: 1 }, { unique: true })")
		}
		spec := bson.D{{Key: "key", Value: keys}}
		o, err := argDoc(c, 1, "options")
		if err != nil {
			return err
		}
		if _, ok := lookup(o, "name"); !ok {
			spec = append(spec, bson.E{Key: "name", Value: indexName(keys)})
		}
		spec = append(spec, o...)
		return e.runCommand(ctx, e.db, bson.D{{Key: "createIndexes", Value: name}, {Key: "indexes", Value: bson.A{spec}}})
	case "dropIndex":
		if len(c.Args) == 0 {
			return errors.New("usage: dropIndex('name_1')")
		}
		return e.runCommand(ctx, e.db, bson.D{{Key: "dropIndexes", Value: name}, {Key: "index", Value: c.Args[0]}})
	case "insertOne":
		doc, err := argDoc(c, 0, "document")
		if err != nil {
			return err
		}
		res, err := coll.InsertOne(ctx, doc)
		if err != nil {
			return err
		}
		e.info(fmt.Sprintf("{ acknowledged: true, insertedId: %s }", formatAny(res.InsertedID)))
		return nil
	case "insertMany":
		arr, ok := argAt(c, 0).(bson.A)
		if !ok {
			return errors.New("insertMany() takes an array of documents")
		}
		docs := make([]any, len(arr))
		for i, d := range arr {
			if _, ok := d.(bson.D); !ok {
				return fmt.Errorf("insertMany(): item %d is not a document", i+1)
			}
			docs[i] = d
		}
		res, err := coll.InsertMany(ctx, docs)
		if err != nil {
			return err
		}
		e.info(fmt.Sprintf("{ acknowledged: true, insertedCount: %d }", len(res.InsertedIDs)))
		return nil
	case "updateOne", "updateMany", "replaceOne":
		filter, err := argDoc(c, 0, "filter")
		if err != nil {
			return err
		}
		if len(c.Args) < 2 {
			return fmt.Errorf("usage: %s(filter, update)", c.Name)
		}
		upd := c.Args[1]
		o, err := argDoc(c, 2, "options")
		if err != nil {
			return err
		}
		upsert := false
		if v, ok := lookup(o, "upsert"); ok {
			upsert, _ = v.(bool)
		}
		var res *mongo.UpdateResult
		switch c.Name {
		case "updateOne":
			uo := options.UpdateOne().SetUpsert(upsert)
			if af, ok := lookup(o, "arrayFilters"); ok {
				if a, ok := af.(bson.A); ok {
					uo.SetArrayFilters([]any(a))
				}
			}
			res, err = coll.UpdateOne(ctx, filter, upd, uo)
		case "updateMany":
			uo := options.UpdateMany().SetUpsert(upsert)
			if af, ok := lookup(o, "arrayFilters"); ok {
				if a, ok := af.(bson.A); ok {
					uo.SetArrayFilters([]any(a))
				}
			}
			res, err = coll.UpdateMany(ctx, filter, upd, uo)
		default:
			if _, ok := upd.(bson.D); !ok {
				return errors.New("replaceOne(): the replacement must be a document")
			}
			res, err = coll.ReplaceOne(ctx, filter, upd, options.Replace().SetUpsert(upsert))
		}
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("{ acknowledged: true, matchedCount: %d, modifiedCount: %d, upsertedCount: %d", res.MatchedCount, res.ModifiedCount, res.UpsertedCount)
		if res.UpsertedID != nil {
			msg += ", upsertedId: " + formatAny(res.UpsertedID)
		}
		e.info(msg + " }")
		return nil
	case "deleteOne", "deleteMany":
		filter, err := argDoc(c, 0, "filter")
		if err != nil {
			return err
		}
		var res *mongo.DeleteResult
		if c.Name == "deleteOne" {
			res, err = coll.DeleteOne(ctx, filter)
		} else {
			res, err = coll.DeleteMany(ctx, filter)
		}
		if err != nil {
			return err
		}
		e.info(fmt.Sprintf("{ acknowledged: true, deletedCount: %d }", res.DeletedCount))
		return nil
	case "drop":
		if err := coll.Drop(ctx); err != nil {
			return err
		}
		e.info("dropped collection " + name)
		return nil
	case "stats":
		return e.runCommand(ctx, e.db, bson.D{{Key: "collStats", Value: name}})
	}
	return fmt.Errorf("%s() is not supported. Supported: find, findOne, aggregate, countDocuments, estimatedDocumentCount, distinct, insertOne, insertMany, updateOne, updateMany, replaceOne, deleteOne, deleteMany, getIndexes, createIndex, dropIndex, stats, drop", c.Name)
}

func argAt(c Call, i int) any {
	if i < len(c.Args) {
		return c.Args[i]
	}
	return nil
}

func indexName(keys bson.D) string {
	parts := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		parts = append(parts, k.Key, strings.Trim(formatAny(k.Value), "'"))
	}
	return strings.Join(parts, "_")
}

func (e *executor) find(ctx context.Context, coll *mongo.Collection, c Call, mods []Call, info DocsInfo) error {
	filter, err := argDoc(c, 0, "filter")
	if err != nil {
		return err
	}
	proj, err := argDoc(c, 1, "projection")
	if err != nil {
		return err
	}
	o := options.Find()
	limit := int64(-1)
	var skip int64
	var sortDoc bson.D
	for _, m := range mods {
		switch m.Name {
		case "sort":
			d, err := argDoc(m, 0, "sort")
			if err != nil {
				return err
			}
			o.SetSort(d)
			sortDoc = d
		case "limit":
			n, err := argInt(m, 0)
			if err != nil {
				return err
			}
			if n < 0 {
				n = -n
			}
			limit = n
		case "skip":
			n, err := argInt(m, 0)
			if err != nil {
				return err
			}
			skip = n
			o.SetSkip(n)
		case "projection", "project":
			d, err := argDoc(m, 0, "projection")
			if err != nil {
				return err
			}
			proj = d
		case "hint":
			if len(m.Args) > 0 {
				o.SetHint(m.Args[0])
			}
		case "collation":
			d, err := argDoc(m, 0, "collation")
			if err != nil {
				return err
			}
			o.SetCollation(collationFrom(d))
		case "maxTimeMS", "comment", "batchSize", "pretty", "toArray", "allowDiskUse":
			// accepted for compatibility; no effect on results
		case "count", "size", "itcount":
			n, err := coll.CountDocuments(ctx, filter)
			if err != nil {
				return err
			}
			return e.emitScalar(intValue(n))
		case "explain":
			cmd := bson.D{{Key: "find", Value: coll.Name()}, {Key: "filter", Value: filter}}
			if len(proj) > 0 {
				cmd = append(cmd, bson.E{Key: "projection", Value: proj})
			}
			if sortDoc != nil {
				cmd = append(cmd, bson.E{Key: "sort", Value: sortDoc})
			}
			if limit >= 0 {
				cmd = append(cmd, bson.E{Key: "limit", Value: limit})
			}
			if skip > 0 {
				cmd = append(cmd, bson.E{Key: "skip", Value: skip})
			}
			verbosity := "queryPlanner"
			if s, ok := argAt(m, 0).(string); ok {
				verbosity = s
			}
			return e.runCommand(ctx, e.db, bson.D{{Key: "explain", Value: cmd}, {Key: "verbosity", Value: verbosity}})
		default:
			return fmt.Errorf("find() cannot be followed by .%s()", m.Name)
		}
	}
	if len(proj) > 0 {
		o.SetProjection(proj)
	}
	max := int64(e.maxDocs())
	applied := limit
	if limit < 0 || limit == 0 {
		applied = DefaultFindLimit
		if int64(DefaultFindLimit) > max {
			applied = max
		}
		info.Limit = int(applied)
	} else if limit > max {
		applied = max
		info.Limit = int(max)
	}
	if limit > 0 && limit <= max {
		o.SetLimit(applied) // explicit limit: exactly what was asked for
	} else {
		o.SetLimit(applied + 1) // +1 detects that more documents exist
	}
	cur, err := coll.Find(ctx, filter, o)
	if err != nil {
		return err
	}
	info.Editable = len(proj) == 0
	return e.drainCursor(ctx, cur, info, int(applied))
}

func collationFrom(d bson.D) *options.Collation {
	c := &options.Collation{}
	for _, el := range d {
		switch el.Key {
		case "locale":
			c.Locale, _ = el.Value.(string)
		case "strength":
			n, _ := asInt64(el.Value)
			c.Strength = int(n)
		case "caseLevel":
			c.CaseLevel, _ = el.Value.(bool)
		case "numericOrdering":
			c.NumericOrdering, _ = el.Value.(bool)
		}
	}
	return c
}

// drainCursor reads up to limit documents (0 = maxDocs) and emits them.
func (e *executor) drainCursor(ctx context.Context, cur *mongo.Cursor, info DocsInfo, limit int) error {
	defer cur.Close(context.Background())
	if limit <= 0 || limit > e.maxDocs() {
		limit = e.maxDocs()
	}
	var vals []bson.RawValue
	for cur.Next(ctx) {
		if len(vals) >= limit {
			info.Truncated = true
			if info.Limit == 0 {
				info.Limit = limit
			}
			break
		}
		doc := make(bson.Raw, len(cur.Current))
		copy(doc, cur.Current)
		vals = append(vals, bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: doc})
	}
	if err := cur.Err(); err != nil {
		return err
	}
	e.emit(vals, info)
	return nil
}

func (e *executor) emitDoc(raw bson.Raw) {
	e.emit([]bson.RawValue{{Type: bson.TypeEmbeddedDocument, Value: raw}}, DocsInfo{})
}

func (e *executor) emitScalar(v any) error {
	rv, err := toRaw(v)
	if err != nil {
		return err
	}
	e.emit([]bson.RawValue{rv}, DocsInfo{})
	return nil
}

// emit sends one result set: a table (top-level fields as columns) plus the
// documents in shell syntax.
func (e *executor) emit(vals []bson.RawValue, info DocsInfo) {
	e.anyOutput = true
	idx := e.results
	e.results++

	cols, rows := tabulate(vals)
	e.sink.ResultSet(idx, cols)

	info.Docs = make([]string, len(vals))
	info.JSON = make([]string, len(vals))
	info.IDs = make([]string, len(vals))
	for i, v := range vals {
		info.Docs[i] = FormatValue(v, true)
		if d, ok := v.DocumentOK(); ok {
			if j, err := bson.MarshalExtJSON(d, false, false); err == nil {
				info.JSON[i] = string(j)
			}
			if id, err := d.LookupErr("_id"); err == nil {
				info.IDs[i] = FormatValue(id, false)
			}
		} else if j, err := bson.MarshalExtJSON(bson.D{{Key: "value", Value: v}}, false, false); err == nil {
			info.JSON[i] = string(j)
		}
	}
	if info.Editable {
		// Editing needs an _id on every document.
		for _, id := range info.IDs {
			if id == "" {
				info.Editable = false
				break
			}
		}
	}
	e.sink.Docs(idx, info)

	const chunk = 500
	for i := 0; i < len(rows); i += chunk {
		end := i + chunk
		if end > len(rows) {
			end = len(rows)
		}
		e.sink.Rows(idx, rows[i:end])
	}
	e.sink.ResultDone(idx, len(rows), info.Truncated)
	noun := "documents"
	if len(vals) == 1 {
		noun = "document"
	}
	if _, isDoc := firstDoc(vals); !isDoc && len(vals) > 0 {
		noun = "values"
		if len(vals) == 1 {
			noun = "value"
		}
	}
	text := fmt.Sprintf("(%d %s)", len(vals), noun)
	if info.Truncated {
		text += fmt.Sprintf(" — showing the first %d; use .limit() and .skip() to page", len(vals))
	}
	e.sink.Message(engine.Message{Kind: "rows", Text: text, Time: nowMs()})
}

func firstDoc(vals []bson.RawValue) (bson.Raw, bool) {
	for _, v := range vals {
		if d, ok := v.DocumentOK(); ok {
			return d, true
		}
	}
	return nil, false
}

// tabulate flattens results into a grid: top-level fields become columns.
func tabulate(vals []bson.RawValue) ([]engine.Column, [][]any) {
	if _, ok := firstDoc(vals); !ok {
		cols := []engine.Column{{Name: "value", Type: "value"}}
		numeric := len(vals) > 0
		rows := make([][]any, len(vals))
		for i, v := range vals {
			rows[i] = []any{cellText(v)}
			numeric = numeric && (v.IsNumber() || v.Type == bson.TypeNull)
		}
		cols[0].Numeric = numeric
		return cols, rows
	}
	index := map[string]int{}
	var names []string
	types := map[string]string{}
	numeric := map[string]bool{}
	for _, v := range vals {
		d, ok := v.DocumentOK()
		if !ok {
			continue
		}
		elems, _ := d.Elements()
		for _, el := range elems {
			k := el.Key()
			if _, seen := index[k]; !seen {
				index[k] = len(names)
				names = append(names, k)
				numeric[k] = true
			}
			ev := el.Value()
			if ev.Type != bson.TypeNull {
				tn := typeName(ev.Type)
				if t, ok := types[k]; !ok {
					types[k] = tn
				} else if t != tn {
					types[k] = "mixed"
				}
				if !ev.IsNumber() {
					numeric[k] = false
				}
			}
		}
	}
	cols := make([]engine.Column, len(names))
	for i, n := range names {
		t := types[n]
		if t == "" {
			t = "null"
		}
		cols[i] = engine.Column{Name: n, Type: t, Numeric: numeric[n] && t != "null"}
	}
	rows := make([][]any, len(vals))
	for i, v := range vals {
		row := make([]any, len(names))
		if d, ok := v.DocumentOK(); ok {
			elems, _ := d.Elements()
			for _, el := range elems {
				row[index[el.Key()]] = cellText(el.Value())
			}
		} else {
			row[0] = cellText(v)
		}
		rows[i] = row
	}
	return cols, rows
}

// cellText renders a value for a grid cell: strings unquoted, NULL as nil,
// everything else in compact shell syntax.
func cellText(v bson.RawValue) any {
	switch v.Type {
	case bson.TypeNull, bson.TypeUndefined:
		return nil
	case bson.TypeString:
		return v.StringValue()
	case bson.TypeInt64:
		return fmt.Sprint(v.Int64())
	}
	s := FormatValue(v, false)
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return s
}

func typeName(t bson.Type) string {
	switch t {
	case bson.TypeDouble:
		return "double"
	case bson.TypeString:
		return "string"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		return "binData"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeInt32:
		return "int"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDecimal128:
		return "decimal"
	}
	return "value"
}
