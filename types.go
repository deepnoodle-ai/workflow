package workflow

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// typ is a type of Risor's Core (the Risor specification, 05-types.md 2),
// held by the engine. Risor's Go API gives a type only as text: the engine
// writes the types of an expression's names as type text and reads the
// result type back from text, so it keeps its own copy of the model to
// build, join and inspect types (Load composes them from schemas, caught
// errors, set steps and loops).
//
// A type is any, or a set of alternatives with at most one of each kind.
// The zero typ is never: no alternative.
type typ struct {
	any   bool
	null  bool
	bool_ bool
	num   numKind
	str   bool     // a string alternative; lits narrows it to a literal set
	lits  []string // sorted; nil for string
	list  *typ     // the element type of the list alternative, or nil
	rec   *record  // the record of the map alternative, or nil
}

type numKind uint8

const (
	numNone numKind = iota
	numInt
	numFloat
)

// record is a map alternative: fields, and the type of every other key.
// A nil rest is never: the record is exact.
type record struct {
	fields map[string]field
	rest   *typ
}

type field struct {
	t        *typ
	optional bool
}

var (
	tAny   = &typ{any: true}
	tNever = &typ{}
)

func tNull() *typ           { return &typ{null: true} }
func tBool() *typ           { return &typ{bool_: true} }
func tInt() *typ            { return &typ{num: numInt} }
func tFloat() *typ          { return &typ{num: numFloat} }
func tString() *typ         { return &typ{str: true} }
func tList(elem *typ) *typ  { return &typ{list: elem} }
func tLit(vals ...string) *typ {
	l := slices.Clone(vals)
	slices.Sort(l)
	return &typ{str: true, lits: slices.Compact(l)}
}

// tRecord makes an exact record of required fields.
func tRecord(fields map[string]*typ) *typ {
	r := &record{fields: make(map[string]field, len(fields))}
	for k, t := range fields {
		r.fields[k] = field{t: t}
	}
	return &typ{rec: r}
}

func (t *typ) isAny() bool   { return t.any }
func (t *typ) isNever() bool { return !t.any && t.alts() == 0 && !t.null }
func (t *typ) hasNull() bool { return t.any || t.null }

// isNull reports whether the type is exactly null.
func (t *typ) isNull() bool { return !t.any && t.null && t.alts() == 0 }

// alts counts the alternatives other than null.
func (t *typ) alts() int {
	n := 0
	for _, b := range []bool{t.bool_, t.num != numNone, t.str, t.list != nil, t.rec != nil} {
		if b {
			n++
		}
	}
	return n
}

// union reports whether the type has two or more kinds besides null.
func (t *typ) union() bool { return !t.any && t.alts() > 1 }

func (t *typ) clone() *typ {
	c := *t
	return &c
}

func (t *typ) nonNull() *typ {
	if t.any || !t.null {
		return t
	}
	c := t.clone()
	c.null = false
	return c
}

func (t *typ) orNull() *typ {
	if t.any || t.null {
		return t
	}
	c := t.clone()
	c.null = true
	return c
}

// joinTypes is Join (05-types.md 4.1): the type of a value of type a or b. A nil
// side is the start of a fold: joinTypes(nil, b) is b.
func joinTypes(a, b *typ) *typ {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.any || b.any:
		return tAny
	}
	out := &typ{null: a.null || b.null, bool_: a.bool_ || b.bool_, num: max(a.num, b.num)}
	switch {
	case a.str && b.str:
		out.str = true
		if a.lits != nil && b.lits != nil {
			out.lits = tLit(append(slices.Clone(a.lits), b.lits...)...).lits
		}
	case a.str:
		out.str, out.lits = true, a.lits
	case b.str:
		out.str, out.lits = true, b.lits
	}
	switch {
	case a.list != nil && b.list != nil:
		out.list = joinTypes(a.list, b.list)
	case a.list != nil:
		out.list = a.list
	default:
		out.list = b.list
	}
	switch {
	case a.rec != nil && b.rec != nil:
		out.rec = joinRecords(a.rec, b.rec)
	case a.rec != nil:
		out.rec = a.rec
	default:
		out.rec = b.rec
	}
	return out
}

func joinRecords(a, b *record) *record {
	out := &record{fields: map[string]field{}}
	for k, fa := range a.fields {
		if fb, ok := b.fields[k]; ok {
			out.fields[k] = field{t: joinTypes(fa.t, fb.t), optional: fa.optional || fb.optional}
			continue
		}
		ft := fa.t
		if b.rest != nil {
			ft = joinTypes(ft, b.rest)
		}
		out.fields[k] = field{t: ft, optional: true}
	}
	for k, fb := range b.fields {
		if _, ok := a.fields[k]; ok {
			continue
		}
		ft := fb.t
		if a.rest != nil {
			ft = joinTypes(ft, a.rest)
		}
		out.fields[k] = field{t: ft, optional: true}
	}
	out.rest = joinTypes(a.rest, b.rest)
	return out
}

func sameType(a, b *typ) bool { return a.String() == b.String() }

// String prints the type as Risor prints it (05-types.md 3.9). The text
// reads back as the same type, in Risor and in parseType.
func (t *typ) String() string {
	if t.any {
		return "any"
	}
	parts := t.parts()
	if len(parts) == 0 {
		return "never"
	}
	return strings.Join(parts, " | ")
}

// parts are the printed alternatives, a literal set counting one per string.
func (t *typ) parts() []string {
	var out []string
	if t.bool_ {
		out = append(out, "bool")
	}
	switch t.num {
	case numInt:
		out = append(out, "int")
	case numFloat:
		out = append(out, "float")
	}
	if t.str {
		if t.lits == nil {
			out = append(out, "string")
		}
		for _, s := range t.lits {
			out = append(out, quoteJSON(s))
		}
	}
	if t.list != nil {
		e := t.list.String()
		if !t.list.any && len(t.list.parts()) > 1 {
			e = "(" + e + ")"
		}
		out = append(out, e+"[]")
	}
	if t.rec != nil {
		out = append(out, t.rec.String())
	}
	if t.null {
		out = append(out, "null")
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *record) String() string {
	if len(r.fields) == 0 {
		if r.rest == nil {
			return "{}"
		}
		return "map<" + r.rest.String() + ">"
	}
	var b strings.Builder
	b.WriteString("{")
	for i, k := range sortedKeys(r.fields) {
		if i > 0 {
			b.WriteString(", ")
		}
		f := r.fields[k]
		if isWord(k) {
			b.WriteString(k)
		} else {
			b.WriteString(quoteJSON(k))
		}
		if f.optional {
			b.WriteString("?")
		}
		b.WriteString(": ")
		b.WriteString(f.t.String())
	}
	switch {
	case r.rest == nil:
	case r.rest.any:
		b.WriteString(", ...")
	default:
		b.WriteString(", ..." + r.rest.String())
	}
	b.WriteString("}")
	return b.String()
}

func isWord(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// quoteJSON is a string's JSON form as Risor writes it (01-language.md 12).
func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteRune(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// parseType reads type text as Risor prints it: the grammar of 05-types.md
// 3.1 without names, variables and signatures.
func parseType(text string) (*typ, error) {
	p := &typeParser{src: text}
	t, err := p.typ()
	if err == nil {
		p.space()
		if p.i < len(p.src) {
			err = fmt.Errorf("unexpected %q", p.src[p.i:])
		}
	}
	if err != nil {
		return nil, fmt.Errorf("type text %q: %w", text, err)
	}
	return t, nil
}

// mustParseType is parseType for text the engine writes itself.
func mustParseType(text string) *typ {
	t, err := parseType(text)
	if err != nil {
		panic(err)
	}
	return t
}

type typeParser struct {
	src string
	i   int
}

func (p *typeParser) space() {
	for p.i < len(p.src) && strings.ContainsRune(" \t\n\r", rune(p.src[p.i])) {
		p.i++
	}
}

func (p *typeParser) peek(s string) bool {
	p.space()
	return strings.HasPrefix(p.src[p.i:], s)
}

func (p *typeParser) eat(s string) bool {
	if p.peek(s) {
		p.i += len(s)
		return true
	}
	return false
}

func (p *typeParser) expect(s string) error {
	if !p.eat(s) {
		return fmt.Errorf("want %q at %d", s, p.i)
	}
	return nil
}

func (p *typeParser) typ() (*typ, error) {
	var out *typ
	for {
		t, err := p.alt()
		if err != nil {
			return nil, err
		}
		out = joinTypes(out, t)
		if !p.eat("|") {
			return out, nil
		}
	}
}

func (p *typeParser) alt() (*typ, error) {
	t, err := p.prim()
	if err != nil {
		return nil, err
	}
	for p.eat("[") {
		if err := p.expect("]"); err != nil {
			return nil, err
		}
		t = tList(t)
	}
	return t, nil
}

func (p *typeParser) word() string {
	p.space()
	start := p.i
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' && p.i > start {
			p.i++
			continue
		}
		break
	}
	return p.src[start:p.i]
}

func (p *typeParser) str() (string, error) {
	p.space()
	if p.i >= len(p.src) || p.src[p.i] != '"' {
		return "", fmt.Errorf("want a string at %d", p.i)
	}
	j := p.i + 1
	for j < len(p.src) && p.src[j] != '"' {
		if p.src[j] == '\\' {
			j++
		}
		j++
	}
	if j >= len(p.src) {
		return "", fmt.Errorf("unterminated string")
	}
	s, err := strconv.Unquote(p.src[p.i : j+1])
	if err != nil || !utf8.ValidString(s) {
		return "", fmt.Errorf("bad string %s", p.src[p.i:j+1])
	}
	p.i = j + 1
	return s, nil
}

func (p *typeParser) prim() (*typ, error) {
	switch {
	case p.eat("("):
		t, err := p.typ()
		if err != nil {
			return nil, err
		}
		return t, p.expect(")")
	case p.peek(`"`):
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return tLit(s), nil
	case p.peek("{"):
		return p.record()
	}
	switch w := p.word(); w {
	case "null":
		return tNull(), nil
	case "bool":
		return tBool(), nil
	case "int":
		return tInt(), nil
	case "float":
		return tFloat(), nil
	case "string":
		return tString(), nil
	case "any":
		return tAny, nil
	case "never":
		return tNever, nil
	case "map":
		if err := p.expect("<"); err != nil {
			return nil, err
		}
		rest, err := p.typ()
		if err != nil {
			return nil, err
		}
		if err := p.expect(">"); err != nil {
			return nil, err
		}
		return &typ{rec: &record{fields: map[string]field{}, rest: rest}}, nil
	default:
		return nil, fmt.Errorf("unexpected %q at %d", w, p.i)
	}
}

func (p *typeParser) record() (*typ, error) {
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	r := &record{fields: map[string]field{}}
	for !p.eat("}") {
		if p.eat("...") {
			r.rest = tAny
			if !p.peek("}") && !p.peek(",") {
				t, err := p.typ()
				if err != nil {
					return nil, err
				}
				r.rest = t
			}
		} else {
			var key string
			if p.peek(`"`) {
				s, err := p.str()
				if err != nil {
					return nil, err
				}
				key = s
			} else if key = p.word(); key == "" {
				return nil, fmt.Errorf("want a key at %d", p.i)
			}
			opt := p.eat("?")
			if err := p.expect(":"); err != nil {
				return nil, err
			}
			t, err := p.typ()
			if err != nil {
				return nil, err
			}
			r.fields[key] = field{t: t, optional: opt}
		}
		if !p.eat(",") {
			if err := p.expect("}"); err != nil {
				return nil, err
			}
			break
		}
	}
	return &typ{rec: r}, nil
}
