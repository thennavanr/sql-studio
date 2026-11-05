// Package mongo adds MongoDB support: connection handling, a mongosh-style
// command parser, BSON <-> shell-syntax conversion and command execution.
package mongo

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// ---------------------------------------------------------------------------
// Printing: BSON -> mongosh syntax. The output is accepted by ParseValue, so a
// document can be viewed, edited and saved back without changing any types.
// ---------------------------------------------------------------------------

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// FormatDoc renders a document. pretty adds newlines and 2-space indentation.
func FormatDoc(doc bson.Raw, pretty bool) string {
	var sb strings.Builder
	writeDoc(&sb, doc, pretty, 0)
	return sb.String()
}

// FormatValue renders any BSON value.
func FormatValue(v bson.RawValue, pretty bool) string {
	var sb strings.Builder
	writeValue(&sb, v, pretty, 0)
	return sb.String()
}

func indent(sb *strings.Builder, n int) {
	for i := 0; i < n; i++ {
		sb.WriteString("  ")
	}
}

func writeKey(sb *strings.Builder, k string) {
	if identRe.MatchString(k) {
		sb.WriteString(k)
	} else {
		sb.WriteString(quote(k))
	}
}

func writeDoc(sb *strings.Builder, doc bson.Raw, pretty bool, depth int) {
	elems, err := doc.Elements()
	if err != nil || len(elems) == 0 {
		sb.WriteString("{}")
		return
	}
	sb.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			sb.WriteByte(',')
		}
		if pretty {
			sb.WriteByte('\n')
			indent(sb, depth+1)
		} else {
			sb.WriteByte(' ')
		}
		writeKey(sb, e.Key())
		sb.WriteString(": ")
		writeValue(sb, e.Value(), pretty, depth+1)
	}
	if pretty {
		sb.WriteByte('\n')
		indent(sb, depth)
	} else {
		sb.WriteByte(' ')
	}
	sb.WriteByte('}')
}

func writeArray(sb *strings.Builder, arr bson.RawArray, pretty bool, depth int) {
	vals, err := arr.Values()
	if err != nil || len(vals) == 0 {
		sb.WriteString("[]")
		return
	}
	// Short arrays of scalars stay on one line even in pretty mode.
	simple := len(vals) <= 8
	for _, v := range vals {
		if v.Type == bson.TypeEmbeddedDocument || v.Type == bson.TypeArray {
			simple = false
			break
		}
	}
	sb.WriteByte('[')
	for i, v := range vals {
		if i > 0 {
			sb.WriteByte(',')
		}
		if pretty && !simple {
			sb.WriteByte('\n')
			indent(sb, depth+1)
		} else if i > 0 {
			sb.WriteByte(' ')
		}
		writeValue(sb, v, pretty, depth+1)
	}
	if pretty && !simple {
		sb.WriteByte('\n')
		indent(sb, depth)
	}
	sb.WriteByte(']')
}

func writeValue(sb *strings.Builder, v bson.RawValue, pretty bool, depth int) {
	switch v.Type {
	case bson.TypeEmbeddedDocument:
		writeDoc(sb, v.Document(), pretty, depth)
	case bson.TypeArray:
		writeArray(sb, v.Array(), pretty, depth)
	case bson.TypeString:
		sb.WriteString(quote(v.StringValue()))
	case bson.TypeInt32:
		sb.WriteString(strconv.FormatInt(int64(v.Int32()), 10))
	case bson.TypeInt64:
		fmt.Fprintf(sb, "Long('%d')", v.Int64())
	case bson.TypeDouble:
		sb.WriteString(formatDouble(v.Double()))
	case bson.TypeDecimal128:
		fmt.Fprintf(sb, "Decimal128('%s')", v.Decimal128().String())
	case bson.TypeBoolean:
		sb.WriteString(strconv.FormatBool(v.Boolean()))
	case bson.TypeNull:
		sb.WriteString("null")
	case bson.TypeUndefined:
		sb.WriteString("undefined")
	case bson.TypeObjectID:
		fmt.Fprintf(sb, "ObjectId('%s')", v.ObjectID().Hex())
	case bson.TypeDateTime:
		sb.WriteString(formatDate(v.DateTime()))
	case bson.TypeTimestamp:
		t, i := v.Timestamp()
		fmt.Fprintf(sb, "Timestamp({ t: %d, i: %d })", t, i)
	case bson.TypeRegex:
		p, o := v.Regex()
		sb.WriteString("/" + strings.ReplaceAll(p, "/", `\/`) + "/" + o)
	case bson.TypeBinary:
		st, data := v.Binary()
		if st == bson.TypeBinaryUUID && len(data) == 16 {
			h := hex.EncodeToString(data)
			fmt.Fprintf(sb, "UUID('%s-%s-%s-%s-%s')", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
		} else {
			fmt.Fprintf(sb, "BinData(%d, '%s')", st, base64.StdEncoding.EncodeToString(data))
		}
	case bson.TypeMinKey:
		sb.WriteString("MinKey()")
	case bson.TypeMaxKey:
		sb.WriteString("MaxKey()")
	case bson.TypeJavaScript:
		fmt.Fprintf(sb, "Code(%s)", quote(v.JavaScript()))
	case bson.TypeSymbol:
		sb.WriteString(quote(v.Symbol()))
	default:
		sb.WriteString(quote(v.String()))
	}
}

func formatDouble(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	// Keep a decimal point so the value parses back as a double, not an int.
	if !strings.ContainsAny(s, ".eEn") {
		s += ".0"
	}
	return s
}

func formatDate(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	return "ISODate('" + t.Format("2006-01-02T15:04:05.000Z") + "')"
}

func quote(s string) string {
	var sb strings.Builder
	sb.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'':
			sb.WriteString(`\'`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x2028 || r == 0x2029 {
				fmt.Fprintf(&sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('\'')
	return sb.String()
}

// ---------------------------------------------------------------------------
// Lexer
// ---------------------------------------------------------------------------

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tString
	tNumber
	tRegex
	tPunct
)

type token struct {
	kind tokKind
	text string // identifier, string contents, number text, punctuation, regex pattern
	opts string // regex flags
	pos  int    // byte offset
}

// SyntaxError reports a parse problem with its position in the input.
type SyntaxError struct {
	Msg  string
	Pos  int
	Line int
	Col  int
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Col, e.Msg)
}

type lexer struct {
	src  string
	pos  int
	toks []token
}

func lineCol(src string, pos int) (int, int) {
	line, col := 1, 1
	for i := 0; i < pos && i < len(src); i++ {
		if src[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func (l *lexer) errAt(pos int, format string, a ...any) error {
	line, col := lineCol(l.src, pos)
	return &SyntaxError{Msg: fmt.Sprintf(format, a...), Pos: pos, Line: line, Col: col}
}

// regexAllowed reports whether a '/' at this point starts a regex literal
// (rather than being division, which we do not support anyway).
func (l *lexer) regexAllowed() bool {
	if len(l.toks) == 0 {
		return true
	}
	last := l.toks[len(l.toks)-1]
	return last.kind == tPunct && strings.Contains("({[,:;", last.text)
}

func lex(src string) ([]token, error) {
	l := &lexer{src: src}
	for {
		// skip whitespace and comments
		for l.pos < len(src) {
			c := src[l.pos]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				l.pos++
			} else if strings.HasPrefix(src[l.pos:], "//") {
				for l.pos < len(src) && src[l.pos] != '\n' {
					l.pos++
				}
			} else if strings.HasPrefix(src[l.pos:], "/*") {
				end := strings.Index(src[l.pos+2:], "*/")
				if end < 0 {
					return nil, l.errAt(l.pos, "unterminated comment")
				}
				l.pos += end + 4
			} else {
				break
			}
		}
		if l.pos >= len(src) {
			l.toks = append(l.toks, token{kind: tEOF, pos: l.pos})
			return l.toks, nil
		}
		start := l.pos
		c := src[l.pos]
		switch {
		case c == '\'' || c == '"' || c == '`':
			s, err := l.readString(c)
			if err != nil {
				return nil, err
			}
			l.toks = append(l.toks, token{kind: tString, text: s, pos: start})
		case c == '/' && l.regexAllowed():
			p, o, err := l.readRegex()
			if err != nil {
				return nil, err
			}
			l.toks = append(l.toks, token{kind: tRegex, text: p, opts: o, pos: start})
		case c >= '0' && c <= '9' || (c == '.' && l.pos+1 < len(src) && src[l.pos+1] >= '0' && src[l.pos+1] <= '9'):
			l.toks = append(l.toks, token{kind: tNumber, text: l.readNumber(), pos: start})
		case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			for l.pos < len(src) {
				ch := src[l.pos]
				if ch == '_' || ch == '$' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
					l.pos++
				} else {
					break
				}
			}
			l.toks = append(l.toks, token{kind: tIdent, text: src[start:l.pos], pos: start})
		case strings.IndexByte("{}[](),:.;-+", c) >= 0:
			l.pos++
			l.toks = append(l.toks, token{kind: tPunct, text: string(c), pos: start})
		default:
			return nil, l.errAt(start, "unexpected character %q", c)
		}
	}
}

func (l *lexer) readString(q byte) (string, error) {
	start := l.pos
	l.pos++
	var sb strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == q {
			l.pos++
			return sb.String(), nil
		}
		if c == '\n' && q != '`' {
			break
		}
		if c == '\\' && l.pos+1 < len(l.src) {
			l.pos++
			e := l.src[l.pos]
			switch e {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'v':
				sb.WriteByte('\v')
			case '0':
				sb.WriteByte(0)
			case 'u':
				if l.pos+4 < len(l.src) {
					if n, err := strconv.ParseUint(l.src[l.pos+1:l.pos+5], 16, 32); err == nil {
						r := rune(n)
						l.pos += 4
						// Combine UTF-16 surrogate pairs.
						if r >= 0xD800 && r <= 0xDBFF && strings.HasPrefix(l.src[l.pos+1:], `\u`) && l.pos+6 < len(l.src) {
							if n2, err := strconv.ParseUint(l.src[l.pos+3:l.pos+7], 16, 32); err == nil && n2 >= 0xDC00 && n2 <= 0xDFFF {
								r = 0x10000 + (r-0xD800)<<10 + (rune(n2) - 0xDC00)
								l.pos += 6
							}
						}
						sb.WriteRune(r)
						break
					}
				}
				return "", l.errAt(l.pos-1, "invalid \\u escape")
			case 'x':
				if l.pos+2 < len(l.src) {
					if n, err := strconv.ParseUint(l.src[l.pos+1:l.pos+3], 16, 8); err == nil {
						sb.WriteRune(rune(n))
						l.pos += 2
						break
					}
				}
				return "", l.errAt(l.pos-1, "invalid \\x escape")
			case '\n':
				// line continuation
			default:
				sb.WriteByte(e)
			}
			l.pos++
			continue
		}
		sb.WriteByte(c)
		l.pos++
	}
	return "", l.errAt(start, "unterminated string")
}

func (l *lexer) readRegex() (string, string, error) {
	start := l.pos
	l.pos++
	var sb strings.Builder
	inClass := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\n' {
			break
		}
		if c == '\\' && l.pos+1 < len(l.src) {
			if l.src[l.pos+1] == '/' {
				sb.WriteByte('/')
			} else {
				sb.WriteByte(c)
				sb.WriteByte(l.src[l.pos+1])
			}
			l.pos += 2
			continue
		}
		if c == '[' {
			inClass = true
		} else if c == ']' {
			inClass = false
		} else if c == '/' && !inClass {
			l.pos++
			fs := l.pos
			for l.pos < len(l.src) && strings.IndexByte("imsxlu", l.src[l.pos]) >= 0 {
				l.pos++
			}
			return sb.String(), l.src[fs:l.pos], nil
		}
		sb.WriteByte(c)
		l.pos++
	}
	return "", "", l.errAt(start, "unterminated regular expression")
}

func (l *lexer) readNumber() string {
	start := l.pos
	src := l.src
	if strings.HasPrefix(src[l.pos:], "0x") || strings.HasPrefix(src[l.pos:], "0X") {
		l.pos += 2
		for l.pos < len(src) && strings.IndexByte("0123456789abcdefABCDEF", src[l.pos]) >= 0 {
			l.pos++
		}
		return src[start:l.pos]
	}
	for l.pos < len(src) && (src[l.pos] >= '0' && src[l.pos] <= '9' || src[l.pos] == '.' || src[l.pos] == '_') {
		l.pos++
	}
	if l.pos < len(src) && (src[l.pos] == 'e' || src[l.pos] == 'E') {
		l.pos++
		if l.pos < len(src) && (src[l.pos] == '+' || src[l.pos] == '-') {
			l.pos++
		}
		for l.pos < len(src) && src[l.pos] >= '0' && src[l.pos] <= '9' {
			l.pos++
		}
	}
	return src[start:l.pos]
}

// ---------------------------------------------------------------------------
// Parser: mongosh literals -> BSON values
// ---------------------------------------------------------------------------

type parser struct {
	src  string
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) peekN(n int) token {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}
func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}
func (p *parser) isPunct(s string) bool {
	t := p.peek()
	return t.kind == tPunct && t.text == s
}
func (p *parser) err(t token, format string, a ...any) error {
	line, col := lineCol(p.src, t.pos)
	return &SyntaxError{Msg: fmt.Sprintf(format, a...), Pos: t.pos, Line: line, Col: col}
}
func (p *parser) expect(s string) error {
	t := p.next()
	if t.kind != tPunct || t.text != s {
		return p.err(t, "expected %q but found %s", s, describe(t))
	}
	return nil
}

func describe(t token) string {
	switch t.kind {
	case tEOF:
		return "end of input"
	case tString:
		return "a string"
	case tNumber:
		return "number " + t.text
	case tIdent:
		return "'" + t.text + "'"
	case tRegex:
		return "a regular expression"
	}
	return "'" + t.text + "'"
}

// ParseValue parses a single mongosh-style value, e.g. `{ a: 1, _id: ObjectId('...') }`.
func ParseValue(src string) (any, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	for p.isPunct(";") {
		p.next()
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, p.err(t, "unexpected %s after value", describe(t))
	}
	return v, nil
}

// ParseDocument parses a value that must be a document.
func ParseDocument(src string) (bson.D, error) {
	v, err := ParseValue(src)
	if err != nil {
		return nil, err
	}
	d, ok := v.(bson.D)
	if !ok {
		return nil, fmt.Errorf("expected a document like { field: value }")
	}
	return d, nil
}

func (p *parser) value() (any, error) {
	t := p.peek()
	switch t.kind {
	case tString:
		p.next()
		return t.text, nil
	case tNumber:
		p.next()
		return parseNumber(t.text, p, t)
	case tRegex:
		p.next()
		return bson.Regex{Pattern: t.text, Options: sortFlags(t.opts)}, nil
	case tPunct:
		switch t.text {
		case "{":
			return p.object()
		case "[":
			return p.array()
		case "-", "+":
			p.next()
			n := p.peek()
			if n.kind == tIdent && n.text == "Infinity" {
				p.next()
				if t.text == "-" {
					return math.Inf(-1), nil
				}
				return math.Inf(1), nil
			}
			if n.kind != tNumber {
				return nil, p.err(n, "expected a number after %q", t.text)
			}
			p.next()
			if t.text == "-" {
				return parseNumber("-"+n.text, p, n)
			}
			return parseNumber(n.text, p, n)
		}
	case tIdent:
		return p.identValue()
	}
	return nil, p.err(t, "expected a value but found %s", describe(t))
}

func sortFlags(f string) string {
	b := []byte(f)
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j] < b[j-1]; j-- {
			b[j], b[j-1] = b[j-1], b[j]
		}
	}
	return string(b)
}

func parseNumber(text string, p *parser, t token) (any, error) {
	clean := strings.ReplaceAll(text, "_", "")
	if strings.HasPrefix(clean, "0x") || strings.HasPrefix(clean, "-0x") || strings.HasPrefix(clean, "0X") {
		n, err := strconv.ParseInt(strings.Replace(strings.Replace(clean, "0x", "", 1), "0X", "", 1), 16, 64)
		if err != nil {
			return nil, p.err(t, "invalid number %s", text)
		}
		return intValue(n), nil
	}
	if !strings.ContainsAny(clean, ".eE") {
		if n, err := strconv.ParseInt(clean, 10, 64); err == nil {
			return intValue(n), nil
		}
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return nil, p.err(t, "invalid number %s", text)
	}
	return f, nil
}

func intValue(n int64) any {
	if n >= math.MinInt32 && n <= math.MaxInt32 {
		return int32(n)
	}
	return n
}

func (p *parser) object() (any, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	d := bson.D{}
	for !p.isPunct("}") {
		kt := p.next()
		var key string
		switch kt.kind {
		case tIdent, tString, tNumber:
			key = kt.text
		default:
			return nil, p.err(kt, "expected a field name but found %s", describe(kt))
		}
		if err := p.expect(":"); err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		d = append(d, bson.E{Key: key, Value: v})
		if p.isPunct(",") {
			p.next()
			continue
		}
		if !p.isPunct("}") {
			t := p.peek()
			return nil, p.err(t, "expected ',' or '}' but found %s", describe(t))
		}
	}
	p.next()
	return d, nil
}

func (p *parser) array() (any, error) {
	if err := p.expect("["); err != nil {
		return nil, err
	}
	a := bson.A{}
	for !p.isPunct("]") {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		if p.isPunct(",") {
			p.next()
			continue
		}
		if !p.isPunct("]") {
			t := p.peek()
			return nil, p.err(t, "expected ',' or ']' but found %s", describe(t))
		}
	}
	p.next()
	return a, nil
}

// args parses a parenthesised, comma separated argument list.
func (p *parser) args() ([]any, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	var out []any
	for !p.isPunct(")") {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.isPunct(",") {
			p.next()
			continue
		}
		if !p.isPunct(")") {
			t := p.peek()
			return nil, p.err(t, "expected ',' or ')' but found %s", describe(t))
		}
	}
	p.next()
	return out, nil
}

func (p *parser) identValue() (any, error) {
	t := p.next()
	name := t.text
	switch name {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	case "undefined":
		return bson.Undefined{}, nil
	case "NaN":
		return math.NaN(), nil
	case "Infinity":
		return math.Inf(1), nil
	case "new":
		n := p.peek()
		if n.kind != tIdent {
			return nil, p.err(n, "expected a constructor after 'new'")
		}
		return p.identValue()
	}
	if !p.isPunct("(") {
		return nil, p.err(t, "unknown identifier '%s' (strings must be quoted)", name)
	}
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	str := func(i int) (string, bool) {
		if i < len(args) {
			s, ok := args[i].(string)
			return s, ok
		}
		return "", false
	}
	bad := func(usage string) error { return p.err(t, "usage: %s", usage) }
	switch name {
	case "ObjectId", "ObjectID":
		if len(args) == 0 {
			return bson.NewObjectID(), nil
		}
		s, ok := str(0)
		if !ok {
			return nil, bad("ObjectId('24 hex characters')")
		}
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			return nil, p.err(t, "invalid ObjectId %q", s)
		}
		return id, nil
	case "ISODate", "Date":
		if len(args) == 0 {
			return bson.NewDateTimeFromTime(time.Now()), nil
		}
		if s, ok := str(0); ok {
			tm, err := parseDate(s)
			if err != nil {
				return nil, p.err(t, "invalid date %q", s)
			}
			return bson.NewDateTimeFromTime(tm), nil
		}
		if ms, ok := asInt64(args[0]); ok {
			return bson.DateTime(ms), nil
		}
		return nil, bad("ISODate('2024-01-31T12:00:00Z')")
	case "NumberLong", "Long":
		if len(args) == 1 {
			if s, ok := str(0); ok {
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return nil, p.err(t, "invalid Long %q", s)
				}
				return n, nil
			}
			if n, ok := asInt64(args[0]); ok {
				return n, nil
			}
		}
		return nil, bad("Long('123')")
	case "NumberInt", "Int32":
		if len(args) == 1 {
			var n int64
			var ok bool
			if s, isStr := str(0); isStr {
				v, err := strconv.ParseInt(s, 10, 32)
				n, ok = v, err == nil
			} else {
				n, ok = asInt64(args[0])
			}
			if ok && n >= math.MinInt32 && n <= math.MaxInt32 {
				return int32(n), nil
			}
		}
		return nil, bad("Int32(123)")
	case "Double", "NumberDouble":
		if len(args) == 1 {
			if f, ok := asFloat(args[0]); ok {
				return f, nil
			}
			if s, ok := str(0); ok {
				if f, err := strconv.ParseFloat(s, 64); err == nil {
					return f, nil
				}
			}
		}
		return nil, bad("Double(1.5)")
	case "NumberDecimal", "Decimal128":
		s, ok := str(0)
		if !ok {
			if f, isNum := asFloat(args0(args)); isNum {
				s, ok = strconv.FormatFloat(f, 'g', -1, 64), true
			}
		}
		if !ok {
			return nil, bad("Decimal128('1.25')")
		}
		d, err := bson.ParseDecimal128(s)
		if err != nil {
			return nil, p.err(t, "invalid Decimal128 %q", s)
		}
		return d, nil
	case "UUID":
		s, ok := str(0)
		if !ok {
			return nil, bad("UUID('xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx')")
		}
		b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
		if err != nil || len(b) != 16 {
			return nil, p.err(t, "invalid UUID %q", s)
		}
		return bson.Binary{Subtype: bson.TypeBinaryUUID, Data: b}, nil
	case "BinData":
		st, ok1 := asInt64(args0(args))
		s, ok2 := str(1)
		if !ok1 || !ok2 || st < 0 || st > 255 {
			return nil, bad("BinData(0, 'base64')")
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, p.err(t, "invalid base64 in BinData")
		}
		return bson.Binary{Subtype: byte(st), Data: b}, nil
	case "Timestamp":
		if len(args) == 1 {
			if d, ok := args[0].(bson.D); ok {
				var ts bson.Timestamp
				for _, e := range d {
					n, _ := asInt64(e.Value)
					switch e.Key {
					case "t":
						ts.T = uint32(n)
					case "i":
						ts.I = uint32(n)
					}
				}
				return ts, nil
			}
		}
		if len(args) == 2 {
			tt, ok1 := asInt64(args[0])
			ii, ok2 := asInt64(args[1])
			if ok1 && ok2 {
				return bson.Timestamp{T: uint32(tt), I: uint32(ii)}, nil
			}
		}
		return nil, bad("Timestamp({ t: 1700000000, i: 1 })")
	case "MinKey":
		return bson.MinKey{}, nil
	case "MaxKey":
		return bson.MaxKey{}, nil
	case "RegExp":
		s, ok := str(0)
		if !ok {
			return nil, bad("RegExp('pattern', 'i')")
		}
		flags, _ := str(1)
		return bson.Regex{Pattern: s, Options: sortFlags(flags)}, nil
	case "Code":
		s, ok := str(0)
		if !ok {
			return nil, bad("Code('function() {}')")
		}
		return bson.JavaScript(s), nil
	}
	return nil, p.err(t, "unknown function %s()", name)
}

func args0(a []any) any {
	if len(a) > 0 {
		return a[0]
	}
	return nil
}

func asInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		if x == math.Trunc(x) {
			return int64(x), true
		}
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date")
}

// ---------------------------------------------------------------------------
// Statements: db.<collection>.<method>(...).<modifier>(...), use, show
// ---------------------------------------------------------------------------

// Call is one method invocation in a chain.
type Call struct {
	Name string
	Args []any
}

// Statement is one parsed shell command.
type Statement struct {
	Kind       string // "use", "show", "db" (database method), "collection"
	Arg        string // database name for use; target for show
	Collection string
	Chain      []Call // first is the method; the rest are cursor modifiers
	Start, End int    // byte range in the script
	Line       int
}

// ParseScript parses one or more shell statements.
func ParseScript(src string) ([]Statement, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	var out []Statement
	for {
		for p.isPunct(";") {
			p.next()
		}
		t := p.peek()
		if t.kind == tEOF {
			return out, nil
		}
		st, err := p.statement()
		if err != nil {
			return nil, err
		}
		st.Start = t.pos
		st.End = p.peek().pos
		if prev := p.toks[p.i-1]; prev.kind != tEOF {
			st.End = prev.pos + 1
		}
		st.Line, _ = lineCol(src, t.pos)
		out = append(out, st)
	}
}

func (p *parser) statement() (Statement, error) {
	t := p.next()
	if t.kind != tIdent {
		return Statement{}, p.err(t, "expected a command such as db.collection.find() but found %s", describe(t))
	}
	switch t.text {
	case "use":
		n := p.next()
		if n.kind != tIdent && n.kind != tString {
			return Statement{}, p.err(n, "usage: use <database>")
		}
		name := n.text
		// Allow names with dashes such as my-db.
		for p.isPunct("-") && p.peekN(1).kind == tIdent {
			p.next()
			name += "-" + p.next().text
		}
		return Statement{Kind: "use", Arg: name}, nil
	case "show":
		n := p.next()
		if n.kind != tIdent {
			return Statement{}, p.err(n, "usage: show dbs | show collections")
		}
		switch n.text {
		case "dbs", "databases":
			return Statement{Kind: "show", Arg: "dbs"}, nil
		case "collections", "tables":
			return Statement{Kind: "show", Arg: "collections"}, nil
		}
		return Statement{}, p.err(n, "usage: show dbs | show collections")
	case "db":
	default:
		return Statement{}, p.err(t, "commands must start with db., use or show")
	}

	// db.<a>.<b>...<method>(...)  or  db.getCollection('x').<method>(...)  or  db.<method>(...)
	var names []string
	st := Statement{}
	for {
		if err := p.expect("."); err != nil {
			return Statement{}, err
		}
		n := p.next()
		if n.kind != tIdent {
			return Statement{}, p.err(n, "expected a name after '.'")
		}
		if p.isPunct("(") {
			args, err := p.args()
			if err != nil {
				return Statement{}, err
			}
			if len(names) == 0 && (n.text == "getCollection" || n.text == "getSiblingDB") {
				s, ok := args0(args).(string)
				if !ok {
					return Statement{}, p.err(n, "usage: db.%s('name')", n.text)
				}
				if n.text == "getSiblingDB" {
					return Statement{}, p.err(n, "getSiblingDB is not supported; use 'use %s' instead", s)
				}
				names = append(names, s)
				st.Kind = "collection"
				st.Collection = s
				continue
			}
			call := Call{Name: n.text, Args: args}
			if st.Kind == "" {
				if len(names) == 0 {
					st.Kind = "db"
				} else {
					st.Kind = "collection"
					st.Collection = strings.Join(names, ".")
				}
			}
			st.Chain = append(st.Chain, call)
			if !p.isPunct(".") {
				return st, nil
			}
			continue
		}
		// Property access is only meaningful while naming the collection
		// (db.system.profile); after a call or getCollection() it must be a method.
		if len(st.Chain) > 0 || st.Kind == "collection" {
			return Statement{}, p.err(n, "expected '(' after %s", n.text)
		}
		names = append(names, n.text)
		if !p.isPunct(".") {
			return Statement{}, p.err(p.peek(), "expected a method call such as db.%s.find()", strings.Join(names, "."))
		}
	}
}
