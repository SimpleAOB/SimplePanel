package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MongoQuery is a parsed mongosh-style query such as
//
//	db.orders.find({ status: "paid", total: { $gt: 100 } }).sort({ createdAt: -1 }).limit(20)
//
// Only the shape is accepted: a collection, one read method and literal
// arguments (objects, arrays, strings, numbers, ObjectId(), ISODate(), /regex/…).
// Arguments become Extended JSON, so nothing the user types is run as code.
type MongoQuery struct {
	DB         string
	Collection string
	Method     string
	Args       []json.RawMessage
	Sort       json.RawMessage
	Projection json.RawMessage
	Skip       int64
	Limit      int
}

var mongoMethods = map[string]bool{
	"find": true, "findOne": true, "aggregate": true, "countDocuments": true,
	"estimatedDocumentCount": true, "distinct": true, "count": true,
}

// ParseMongoQuery parses q.
func ParseMongoQuery(q string) (*MongoQuery, error) {
	q = strings.TrimSpace(q)
	for strings.HasSuffix(q, ";") {
		q = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	}
	if q == "" {
		return nil, errors.New("query is empty")
	}
	if len(q) > 20000 || strings.ContainsRune(q, 0) {
		return nil, errors.New("query is too long or has a NUL byte")
	}
	p := &mparser{s: q}
	mq := &MongoQuery{}
	if err := p.parse(mq); err != nil {
		return nil, fmt.Errorf("%v (write queries like db.users.find({ age: { $gt: 30 } }).limit(10))", err)
	}
	if mq.Method == "count" {
		mq.Method = "countDocuments"
	}
	if mq.Method == "distinct" && len(mq.Args) == 0 {
		return nil, errors.New("distinct needs a field name")
	}
	if mq.Method == "aggregate" {
		for _, a := range mq.Args {
			s := string(a)
			if strings.Contains(s, `"$out"`) || strings.Contains(s, `"$merge"`) {
				return nil, errors.New("$out and $merge write data; the Query tab is read-only")
			}
		}
	}
	return mq, nil
}

type mparser struct {
	s   string
	pos int
}

func (p *mparser) errf(format string, a ...any) error {
	return fmt.Errorf("at character %d: %s", p.pos+1, fmt.Sprintf(format, a...))
}

func (p *mparser) ws() {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			p.pos++
		} else if strings.HasPrefix(p.s[p.pos:], "//") {
			for p.pos < len(p.s) && p.s[p.pos] != '\n' {
				p.pos++
			}
		} else {
			return
		}
	}
}

func (p *mparser) peek() byte {
	p.ws()
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *mparser) expect(c byte) error {
	if p.peek() != c {
		if p.pos >= len(p.s) {
			return p.errf("expected %q but the query ended", c)
		}
		return p.errf("expected %q", c)
	}
	p.pos++
	return nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentChar(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' || c == '-' }

func (p *mparser) ident() string {
	p.ws()
	start := p.pos
	if p.pos < len(p.s) && isIdentStart(p.s[p.pos]) {
		p.pos++
		for p.pos < len(p.s) && (isIdentStart(p.s[p.pos]) || p.s[p.pos] >= '0' && p.s[p.pos] <= '9') {
			p.pos++
		}
	}
	return p.s[start:p.pos]
}

func (p *mparser) parse(mq *MongoQuery) error {
	if p.ident() != "db" {
		return p.errf("start with db.<collection>")
	}
	if p.peek() != '[' {
		if err := p.expect('.'); err != nil {
			return err
		}
	}
	// db.getSiblingDB("x").…  db.getCollection("x").…  db.x.…  db["x"].…
	for {
		save := p.pos
		name := p.ident()
		if name == "getSiblingDB" || name == "getCollection" {
			args, err := p.args()
			if err != nil {
				return err
			}
			var s string
			if len(args) != 1 || json.Unmarshal(args[0], &s) != nil || s == "" {
				return p.errf("%s needs a name in quotes", name)
			}
			if name == "getSiblingDB" {
				if !mongoNameOK(s) {
					return p.errf("bad database name")
				}
				mq.DB = s
				if err := p.expect('.'); err != nil {
					return err
				}
				continue
			}
			mq.Collection = s
			break
		}
		if name == "" && p.peek() == '[' {
			p.pos++
			v, err := p.value()
			if err != nil {
				return err
			}
			if err := json.Unmarshal(v, &mq.Collection); err != nil {
				return p.errf("expected a collection name in quotes")
			}
			if err := p.expect(']'); err != nil {
				return err
			}
			break
		}
		if name == "" {
			p.pos = save
			return p.errf("expected a collection name")
		}
		// Collection names may contain dots: db.app.events.find()
		mq.Collection = name
		for {
			save := p.pos
			if p.peek() != '.' {
				break
			}
			p.pos++
			next := p.ident()
			p.ws()
			if p.peek() == '(' {
				if mongoMethods[next] {
					p.pos = save
					break
				}
				return p.errf("unsupported method %q; the Query tab is read-only, so use find, findOne, aggregate, countDocuments, estimatedDocumentCount or distinct", next)
			}
			if next == "" {
				return p.errf("expected a name after the dot")
			}
			mq.Collection += "." + next
		}
		break
	}
	if mq.Collection == "" || len(mq.Collection) > 200 || strings.HasPrefix(mq.Collection, "system.") {
		return p.errf("bad collection name")
	}
	if err := p.expect('.'); err != nil {
		return err
	}
	mq.Method = p.ident()
	if !mongoMethods[mq.Method] {
		return p.errf("unsupported method %q; use find, findOne, aggregate, countDocuments, estimatedDocumentCount or distinct", mq.Method)
	}
	args, err := p.args()
	if err != nil {
		return err
	}
	mq.Args = args
	for p.peek() == '.' {
		p.pos++
		name := p.ident()
		args, err := p.args()
		if err != nil {
			return err
		}
		if mq.Method != "find" && name != "pretty" && name != "toArray" {
			return p.errf(".%s() only works after find()", name)
		}
		switch name {
		case "sort":
			if len(args) != 1 {
				return p.errf("sort takes one object")
			}
			mq.Sort = args[0]
		case "projection", "project":
			if len(args) != 1 {
				return p.errf("projection takes one object")
			}
			mq.Projection = args[0]
		case "limit", "skip":
			var n int64
			if len(args) != 1 || json.Unmarshal(args[0], &n) != nil || n < 0 {
				return p.errf("%s takes a number", name)
			}
			if name == "limit" {
				mq.Limit = int(min(n, QueryMaxRows))
			} else {
				mq.Skip = n
			}
		case "pretty", "toArray":
		default:
			return p.errf("unsupported .%s(); use sort, limit, skip or projection", name)
		}
	}
	if p.peek() != 0 {
		return p.errf("unexpected text after the query; run one query at a time")
	}
	return nil
}

// args parses "(v, v, …)".
func (p *mparser) args() ([]json.RawMessage, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	var out []json.RawMessage
	for p.peek() != ')' {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.peek() == ',' {
			p.pos++
			continue
		}
		if p.peek() != ')' {
			return nil, p.errf("expected ',' or ')'")
		}
	}
	p.pos++
	return out, nil
}

func (p *mparser) value() (json.RawMessage, error) {
	var b strings.Builder
	if err := p.val(&b, 0); err != nil {
		return nil, err
	}
	return json.RawMessage(b.String()), nil
}

func (p *mparser) val(b *strings.Builder, depth int) error {
	if depth > 64 {
		return p.errf("too deeply nested")
	}
	c := p.peek()
	switch {
	case c == '{':
		p.pos++
		b.WriteByte('{')
		first := true
		for p.peek() != '}' {
			if !first {
				b.WriteByte(',')
			}
			first = false
			var key string
			switch k := p.peek(); {
			case k == '"' || k == '\'':
				s, err := p.str()
				if err != nil {
					return err
				}
				key = s
			case isIdentStart(k) || k >= '0' && k <= '9':
				start := p.pos
				for p.pos < len(p.s) && (isIdentChar(p.s[p.pos]) || p.s[p.pos] == '.') {
					p.pos++
				}
				key = p.s[start:p.pos]
			default:
				return p.errf("expected a field name")
			}
			kb, _ := json.Marshal(key)
			b.Write(kb)
			b.WriteByte(':')
			if err := p.expect(':'); err != nil {
				return err
			}
			if err := p.val(b, depth+1); err != nil {
				return err
			}
			if p.peek() == ',' {
				p.pos++
			} else if p.peek() != '}' {
				return p.errf("expected ',' or '}'")
			}
		}
		p.pos++
		b.WriteByte('}')
	case c == '[':
		p.pos++
		b.WriteByte('[')
		first := true
		for p.peek() != ']' {
			if !first {
				b.WriteByte(',')
			}
			first = false
			if err := p.val(b, depth+1); err != nil {
				return err
			}
			if p.peek() == ',' {
				p.pos++
			} else if p.peek() != ']' {
				return p.errf("expected ',' or ']'")
			}
		}
		p.pos++
		b.WriteByte(']')
	case c == '"' || c == '\'':
		s, err := p.str()
		if err != nil {
			return err
		}
		sb, _ := json.Marshal(s)
		b.Write(sb)
	case c == '/':
		return p.regex(b)
	case c == '-' || c == '+' || c == '.' || c >= '0' && c <= '9':
		start := p.pos
		p.pos++
		for p.pos < len(p.s) && strings.IndexByte("0123456789.eE+-", p.s[p.pos]) >= 0 {
			p.pos++
		}
		num := strings.TrimPrefix(p.s[start:p.pos], "+")
		if _, err := strconv.ParseFloat(num, 64); err != nil {
			return p.errf("bad number %q", num)
		}
		if strings.HasPrefix(num, ".") || strings.HasPrefix(num, "-.") {
			num = strings.Replace(num, ".", "0.", 1)
		}
		b.WriteString(num)
	case isIdentStart(c):
		name := p.ident()
		if name == "new" {
			name = p.ident()
		}
		switch name {
		case "true", "false", "null":
			b.WriteString(name)
		case "undefined":
			b.WriteString("null")
		case "ObjectId", "ISODate", "Date", "NumberLong", "NumberInt", "NumberDecimal", "UUID", "Timestamp":
			return p.ctor(b, name)
		default:
			return p.errf("%q isn't allowed here; use literal values", name)
		}
	default:
		if c == 0 {
			return p.errf("the query ended early")
		}
		return p.errf("unexpected %q", c)
	}
	return nil
}

// ctor turns ObjectId("…") and friends into Extended JSON.
func (p *mparser) ctor(b *strings.Builder, name string) error {
	args, err := p.args()
	if err != nil {
		return err
	}
	str := func() (string, bool) {
		if len(args) != 1 {
			return "", false
		}
		var s string
		if json.Unmarshal(args[0], &s) == nil {
			return s, true
		}
		var n json.Number
		if json.Unmarshal(args[0], &n) == nil {
			return n.String(), true
		}
		return "", false
	}
	write := func(key string, v any) {
		vb, _ := json.Marshal(v)
		fmt.Fprintf(b, `{"%s":%s}`, key, vb)
	}
	switch name {
	case "ObjectId":
		s, ok := str()
		if !ok || len(s) != 24 {
			return p.errf("ObjectId needs a 24-character hex string")
		}
		write("$oid", s)
	case "ISODate", "Date":
		if len(args) == 0 {
			return p.errf("%s needs a date", name)
		}
		var n json.Number
		if json.Unmarshal(args[0], &n) == nil {
			write("$date", map[string]string{"$numberLong": n.String()})
			return nil
		}
		s, ok := str()
		if !ok {
			return p.errf("%s needs a date string", name)
		}
		write("$date", s)
	case "NumberLong", "NumberInt", "NumberDecimal":
		s, ok := str()
		if !ok {
			return p.errf("%s needs a number", name)
		}
		write(map[string]string{"NumberLong": "$numberLong", "NumberInt": "$numberInt", "NumberDecimal": "$numberDecimal"}[name], s)
	case "UUID":
		s, ok := str()
		if !ok {
			return p.errf("UUID needs a string")
		}
		write("$uuid", s)
	case "Timestamp":
		if len(args) != 2 {
			return p.errf("Timestamp needs two numbers")
		}
		fmt.Fprintf(b, `{"$timestamp":{"t":%s,"i":%s}}`, args[0], args[1])
	}
	return nil
}

func (p *mparser) str() (string, error) {
	q := p.s[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		p.pos++
		if c == q {
			return b.String(), nil
		}
		if c == '\\' && p.pos < len(p.s) {
			e := p.s[p.pos]
			p.pos++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'u':
				if p.pos+4 <= len(p.s) {
					if r, err := strconv.ParseUint(p.s[p.pos:p.pos+4], 16, 32); err == nil {
						b.WriteRune(rune(r))
						p.pos += 4
						continue
					}
				}
				return "", p.errf("bad \\u escape")
			default:
				b.WriteByte(e)
			}
			continue
		}
		b.WriteByte(c)
	}
	return "", p.errf("unclosed string")
}

func (p *mparser) regex(b *strings.Builder) error {
	p.pos++ // opening /
	var pat strings.Builder
	inClass := false
	for {
		if p.pos >= len(p.s) {
			return p.errf("unclosed regular expression")
		}
		c := p.s[p.pos]
		p.pos++
		if c == '\\' && p.pos < len(p.s) {
			pat.WriteByte(c)
			pat.WriteByte(p.s[p.pos])
			p.pos++
			continue
		}
		if c == '[' {
			inClass = true
		} else if c == ']' {
			inClass = false
		} else if c == '/' && !inClass {
			break
		}
		pat.WriteByte(c)
	}
	start := p.pos
	for p.pos < len(p.s) && strings.IndexByte("imsxu", p.s[p.pos]) >= 0 {
		p.pos++
	}
	pb, _ := json.Marshal(pat.String())
	ob, _ := json.Marshal(p.s[start:p.pos])
	fmt.Fprintf(b, `{"$regularExpression":{"pattern":%s,"options":%s}}`, pb, ob)
	return nil
}
