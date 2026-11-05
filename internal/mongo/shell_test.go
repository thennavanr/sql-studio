package mongo

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func mustDec(s string) bson.Decimal128 {
	d, err := bson.ParseDecimal128(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Every BSON type must survive print -> parse unchanged, so editing a
// document never silently changes its types.
func TestRoundTripAllTypes(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex("65a1b2c3d4e5f60718293a4b")
	doc := bson.D{
		{Key: "_id", Value: oid},
		{Key: "s", Value: "O'Brien \"q\" \\ back\nline\ttab — 日本 😀"},
		{Key: "i32", Value: int32(-42)},
		{Key: "i64small", Value: int64(5)},
		{Key: "i64big", Value: int64(9007199254740993)},
		{Key: "dbl", Value: 3.0},
		{Key: "dbl2", Value: 0.1},
		{Key: "dblbig", Value: 1e300},
		{Key: "nan", Value: math.NaN()},
		{Key: "inf", Value: math.Inf(-1)},
		{Key: "dec", Value: mustDec("12345.6789")},
		{Key: "t", Value: true},
		{Key: "nil", Value: nil},
		{Key: "date", Value: bson.NewDateTimeFromTime(time.Date(2024, 2, 29, 13, 14, 15, 678e6, time.UTC))},
		{Key: "ts", Value: bson.Timestamp{T: 1700000000, I: 7}},
		{Key: "re", Value: bson.Regex{Pattern: `^a/b\d+[/]`, Options: "im"}},
		{Key: "uuid", Value: bson.Binary{Subtype: 4, Data: []byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}}},
		{Key: "bin", Value: bson.Binary{Subtype: 0, Data: []byte{0, 1, 2, 255}}},
		{Key: "min", Value: bson.MinKey{}},
		{Key: "max", Value: bson.MaxKey{}},
		{Key: "code", Value: bson.JavaScript("function () { return 'x'; }")},
		{Key: "weird key.with-dots", Value: int32(1)},
		{Key: "$dollar", Value: int32(2)},
		{Key: "nested", Value: bson.D{{Key: "a", Value: bson.A{int32(1), "two", bson.D{{Key: "three", Value: 3.5}}, bson.A{}}}, {Key: "empty", Value: bson.D{}}}},
		{Key: "arr", Value: bson.A{int32(1), int32(2), int32(3)}},
	}
	want, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, pretty := range []bool{true, false} {
		text := FormatDoc(want, pretty)
		parsed, err := ParseDocument(text)
		if err != nil {
			t.Fatalf("pretty=%v parse error: %v\n%s", pretty, err, text)
		}
		got, err := bson.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("pretty=%v round trip changed the document\nprinted: %s\nwant: %s\ngot:  %s", pretty, text, bson.Raw(want), bson.Raw(got))
		}
	}
	text := FormatDoc(want, true)
	for _, w := range []string{"_id: ObjectId('65a1b2c3d4e5f60718293a4b')", "i64small: Long('5')", "dbl: 3.0", "date: ISODate('2024-02-29T13:14:15.678Z')", "'weird key.with-dots': 1", "uuid: UUID('12345678-9abc-def0-1234-56789abcdef0')", "arr: [1, 2, 3]"} {
		if !strings.Contains(text, w) {
			t.Errorf("printed doc missing %q:\n%s", w, text)
		}
	}
}

func TestParseShellSyntax(t *testing.T) {
	v, err := ParseValue(`{
		// comment
		name: "Ann", 'qty': -5, price: 1.50, big: 3000000000, hex: 0xff,
		when: new Date("2024-01-02"), tags: ["a", /^x/i,], n: NumberLong(7), d: NumberDecimal("0.1"),
		id: ObjectId("65a1b2c3d4e5f60718293a4b"), /* block */ ok: true, none: null,
	}`)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(bson.D)
	m := map[string]any{}
	for _, e := range d {
		m[e.Key] = e.Value
	}
	checks := map[string]any{"name": "Ann", "qty": int32(-5), "price": 1.5, "big": int64(3000000000), "hex": int32(255), "n": int64(7), "ok": true}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("%s = %#v (%T), want %#v", k, m[k], m[k], want)
		}
	}
	if m["when"] != bson.DateTime(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC).UnixMilli()) {
		t.Errorf("when = %v", m["when"])
	}
	if re, ok := m["tags"].(bson.A)[1].(bson.Regex); !ok || re.Pattern != "^x" || re.Options != "i" {
		t.Errorf("regex = %#v", m["tags"])
	}
	if d[len(d)-1].Key != "none" || d[len(d)-1].Value != nil {
		t.Errorf("order/null wrong: %v", d)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		`{ a: 1 `:                "expected ',' or '}'",
		`{ a: foo }`:             "unknown identifier 'foo'",
		`{ a: 'x }`:              "unterminated string",
		`{ a: ObjectId('zz') }`:  "invalid ObjectId",
		"{ a: 1,\n  b: }":        "line 2",
		`{ a: Frobnicate(1) }`:   "unknown function Frobnicate",
		`[1, 2`:                  "expected ',' or ']'",
		`{ a: ISODate('nope') }`: "invalid date",
	}
	for src, want := range cases {
		_, err := ParseValue(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseValue(%q) error = %v, want containing %q", src, err, want)
		}
	}
}

func TestParseScript(t *testing.T) {
	src := `use my-shop
show collections;
db.orders.find({ status: "A" }, { total: 1 })
  .sort({ _id: -1 })
  .limit(20)

// another
db.getCollection('order items').countDocuments({})
db.system.profile.find()
db.runCommand({ ping: 1 }); db.stats()`
	st, err := ParseScript(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 7 {
		t.Fatalf("got %d statements: %+v", len(st), st)
	}
	if st[0].Kind != "use" || st[0].Arg != "my-shop" {
		t.Errorf("use: %+v", st[0])
	}
	if st[1].Kind != "show" || st[1].Arg != "collections" {
		t.Errorf("show: %+v", st[1])
	}
	f := st[2]
	if f.Kind != "collection" || f.Collection != "orders" || len(f.Chain) != 3 || f.Chain[0].Name != "find" || f.Chain[1].Name != "sort" || f.Chain[2].Name != "limit" || f.Line != 3 {
		t.Errorf("find: %+v", f)
	}
	if !strings.HasSuffix(src[f.Start:f.End], ".limit(20)") {
		t.Errorf("find source range = %q", src[f.Start:f.End])
	}
	if st[3].Collection != "order items" || st[3].Chain[0].Name != "countDocuments" {
		t.Errorf("getCollection: %+v", st[3])
	}
	if st[4].Collection != "system.profile" {
		t.Errorf("dotted: %+v", st[4])
	}
	if st[5].Kind != "db" || st[5].Chain[0].Name != "runCommand" || st[6].Chain[0].Name != "stats" {
		t.Errorf("db methods: %+v %+v", st[5], st[6])
	}
	for _, bad := range []string{"db.orders", "select * from x", "db.orders.find(", "db.find().x"} {
		if _, err := ParseScript(bad); err == nil {
			t.Errorf("ParseScript(%q) should fail", bad)
		}
	}
}
