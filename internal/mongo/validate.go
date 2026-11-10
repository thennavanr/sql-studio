package mongo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/thennavanr/sql-studio/internal/engine"
)

// Methods supported by Execute. Validate uses the same lists so the two
// never disagree.
var (
	collectionMethods = set("find", "findOne", "aggregate", "countDocuments", "count", "estimatedDocumentCount",
		"distinct", "insertOne", "insertMany", "updateOne", "updateMany", "replaceOne", "deleteOne", "deleteMany",
		"getIndexes", "createIndex", "dropIndex", "stats", "drop")
	dbMethods = set("getCollectionNames", "getCollectionInfos", "stats", "runCommand", "adminCommand",
		"createCollection", "getName", "version")
	findModifiers = set("sort", "limit", "skip", "projection", "project", "hint", "collation", "maxTimeMS",
		"comment", "batchSize", "pretty", "toArray", "allowDiskUse", "count", "size", "itcount", "explain")
	passiveModifiers = set("pretty", "toArray")
	writeMethods     = set("insertOne", "insertMany", "updateOne", "updateMany", "replaceOne", "createIndex")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func names(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// checkStatement reports a problem with a statement's methods, or nil.
func checkStatement(st Statement) error {
	switch st.Kind {
	case "db":
		c := st.Chain[0]
		if !dbMethods[c.Name] {
			return fmt.Errorf("db.%s() is not supported. Supported: %s", c.Name, names(dbMethods))
		}
		if len(st.Chain) > 1 {
			return fmt.Errorf("db.%s() cannot be followed by .%s()", c.Name, st.Chain[1].Name)
		}
	case "collection":
		c := st.Chain[0]
		if !collectionMethods[c.Name] {
			return fmt.Errorf("%s() is not supported. Supported: %s", c.Name, names(collectionMethods))
		}
		for _, m := range st.Chain[1:] {
			switch {
			case c.Name == "find" && findModifiers[m.Name]:
			case c.Name == "aggregate" && passiveModifiers[m.Name]:
			case passiveModifiers[m.Name]:
			default:
				return fmt.Errorf("%s() cannot be followed by .%s()", c.Name, m.Name)
			}
		}
	}
	return nil
}

// Validate checks a script without running it: syntax, supported methods and
// modifiers, and that the collections it reads exist. Nothing is executed.
func Validate(ctx context.Context, cl *mongo.Client, db, script string) []engine.Message {
	stmts, err := ParseScript(script)
	if err != nil {
		line := 0
		var se *SyntaxError
		if errors.As(err, &se) {
			line = se.Line
		}
		return []engine.Message{{Kind: "error", Text: "Syntax error: " + err.Error(), Line: line, Time: nowMs()}}
	}
	if len(stmts) == 0 {
		return []engine.Message{{Kind: "info", Text: "Nothing to validate.", Time: nowMs()}}
	}
	var msgs []engine.Message
	known := map[string]map[string]bool{} // db -> collections (nil = could not list)
	collectionsOf := func(name string) map[string]bool {
		if c, ok := known[name]; ok {
			return c
		}
		list, err := listCollectionNames(ctx, cl.Database(name))
		if err != nil {
			known[name] = nil
			return nil
		}
		known[name] = set(list...)
		return known[name]
	}
	cur := db
	errorsFound := 0
	for _, st := range stmts {
		if st.Kind == "use" {
			cur = st.Arg
			continue
		}
		if err := checkStatement(st); err != nil {
			errorsFound++
			msgs = append(msgs, engine.Message{Kind: "error", Text: fmt.Sprintf("Line %d: %s", st.Line, err), Line: st.Line, Time: nowMs()})
			continue
		}
		if st.Kind != "collection" || writeMethods[st.Chain[0].Name] {
			continue // writes create collections; nothing to check
		}
		if colls := collectionsOf(cur); colls != nil && !colls[st.Collection] {
			msgs = append(msgs, engine.Message{
				Kind: "info",
				Text: fmt.Sprintf("⚠ Line %d: collection '%s' does not exist in database '%s' (the command will return no documents).", st.Line, st.Collection, cur),
				Line: st.Line,
				Time: nowMs(),
			})
		}
	}
	if errorsFound == 0 {
		msgs = append(msgs, engine.Message{
			Kind: "info",
			Text: fmt.Sprintf("✓ Script is valid — %d statement%s checked. Nothing was executed.", len(stmts), plural(len(stmts))),
			Time: nowMs(),
		})
	}
	return msgs
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
