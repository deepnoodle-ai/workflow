package workflow

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

// Schema is the subset of JSON Schema the library uses to type a run's
// input, step outputs and step parameters.
//
// Wherever a schema is expected, a definition may write a basic type name
// (`output: string`) or the name of a schema declared under `schemas:`
// (`output: findings`).
//
// Two rules differ from JSON Schema, in the direction of catching mistakes:
// expressions may read only declared properties unless
// additionalProperties is true or a schema, and a property not listed in
// required may be nil, so reading through it needs `?.`. At run time,
// undeclared properties are allowed unless additionalProperties is false.
//
// A property's default is the value it takes when it is absent or null, in
// the run's input and in step outputs, so a property with a default is never
// nil. Load checks every default against its schema.
type Schema struct {
	Type        []string           // string, number, integer, boolean, object, array, null
	Description string             //
	Properties  map[string]*Schema //
	Required    []string           //
	Items       *Schema            //
	Enum        []any              //
	Default     any                // nil means none
	// Additional is the schema for undeclared properties; nil means
	// unspecified. AdditionalSet with a nil Additional means false.
	Additional    *Schema
	AdditionalSet bool
	// Ref names a schema under the definition's `schemas:`.
	Ref string

	resolved *Schema
}

var basicTypes = map[string]bool{
	"string": true, "number": true, "integer": true, "boolean": true,
	"object": true, "array": true, "null": true,
}

// ParseSchema reads a JSON Schema document, for the schemas a host returns
// from [KindSchemas]. Refs to named schemas are resolved against the
// definition that uses the schema.
func ParseSchema(data []byte) (*Schema, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("workflow: parse schema: %w", err)
	}
	p := &problems{}
	s := parseSchema(p, "", "schema", v)
	if err := p.err(); err != nil {
		return nil, err
	}
	return s, nil
}

func parseSchema(p *problems, where, key string, v any) *Schema {
	switch t := v.(type) {
	case string:
		if basicTypes[t] {
			return &Schema{Type: []string{t}}
		}
		return &Schema{Ref: t}
	case bool:
		// JSON Schema's true accepts anything, like {}. false accepts
		// nothing, which no step or input can use.
		if !t {
			p.add(where, "%s: the schema false accepts no value", key)
		}
		return &Schema{}
	case map[string]any:
	default:
		p.add(where, "%s must be a schema object or a type or schema name", key)
		return nil
	}
	m := v.(map[string]any)
	s := &Schema{}
	for _, k := range sortedKeys(m) {
		val := m[k]
		at := key + "." + k
		switch k {
		case "type":
			switch tv := val.(type) {
			case string:
				s.Type = []string{tv}
			case []any:
				s.Type = p.strList(where, at, tv)
			default:
				p.add(where, "%s must be a type name or a list of them", at)
			}
			for _, name := range s.Type {
				if !basicTypes[name] {
					p.add(where, "%s: unknown type %q", at, name)
				}
			}
		case "description":
			s.Description = p.str(where, at, val)
		case "properties":
			props := p.obj(where, at, val)
			s.Properties = make(map[string]*Schema, len(props))
			for name, pv := range props {
				s.Properties[name] = parseSchema(p, where, at+"."+name, pv)
			}
		case "required":
			s.Required = p.strList(where, at, val)
		case "items":
			s.Items = parseSchema(p, where, at, val)
		case "enum":
			list, ok := val.([]any)
			if !ok || len(list) == 0 {
				p.add(where, "%s must be a non-empty list", at)
			}
			s.Enum = list
		case "additionalProperties":
			s.AdditionalSet = true
			switch av := val.(type) {
			case bool:
				if av {
					s.Additional = &Schema{}
				}
			default:
				s.Additional = parseSchema(p, where, at, av)
			}
		case "default":
			s.Default = val
		case "$ref":
			s.Ref = p.str(where, at, val)
		default:
			p.add(where, "%s: unknown schema key %q", key, k)
		}
	}
	return s
}

// maxSchemaNodes bounds a schema's size with every ref inlined, which is
// how a host receives it in a command and how it is converted to a type.
// Refs shared down a chain of named schemas can otherwise double the size
// at every level.
const maxSchemaNodes = 10000

// resolveNamedSchemas resolves the refs inside the definition's own
// `schemas:`, in place: Load parsed them, so the definition owns them.
//
// Refs must not form a cycle. An alias cycle (`a: b`, `b: a`) names no
// schema at all, and a recursive schema (a tree node whose children are
// nodes) has no finite expr type and no finite inlined form. Refs into a
// cycle are left unresolved, so the rest of Load still terminates.
func (d *Definition) resolveNamedSchemas(p *problems) {
	refs := map[string][]string{}
	for _, name := range sortedKeys(d.Schemas) {
		refs[name] = schemaRefs(d.Schemas[name])
	}
	cyclic := map[string]bool{}
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var stack []string
	var visit func(string)
	visit = func(name string) {
		state[name] = visiting
		stack = append(stack, name)
		for _, r := range refs[name] {
			switch {
			case d.Schemas[r] == nil:
			case state[r] == visiting:
				i := len(stack) - 1
				for stack[i] != r {
					i--
				}
				cycle := append(append([]string{}, stack[i:]...), r)
				for _, n := range cycle {
					cyclic[n] = true
				}
				p.add("", "schemas.%s: refers to itself (%s); recursive schemas are not supported",
					r, strings.Join(cycle, " → "))
			case state[r] == 0:
				visit(r)
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
	}
	for _, name := range sortedKeys(d.Schemas) {
		if state[name] == 0 {
			visit(name)
		}
	}

	seen := map[*Schema]bool{}
	var walk func(where string, s *Schema)
	walk = func(where string, s *Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		if s.Ref != "" && !cyclic[s.Ref] {
			s.resolved = d.Schemas[s.Ref]
			if s.resolved == nil {
				p.add("", "%s: unknown schema %q", where, s.Ref)
			}
		}
		for _, name := range sortedKeys(s.Properties) {
			walk(where, s.Properties[name])
		}
		walk(where, s.Items)
		walk(where, s.Additional)
	}
	for _, name := range sortedKeys(d.Schemas) {
		walk("schemas."+name, d.Schemas[name])
	}
	for _, name := range sortedKeys(d.Schemas) {
		if !cyclic[name] && d.Schemas[name].inlinedSize() > maxSchemaNodes {
			p.add("", "schemas.%s: has more than %d nodes with its refs inlined", name, maxSchemaNodes)
		}
	}
}

// schemaRefs lists the names s refers to, without following them.
func schemaRefs(s *Schema) []string {
	seen := map[string]bool{}
	var walk func(*Schema)
	walk = func(s *Schema) {
		if s == nil {
			return
		}
		if s.Ref != "" {
			seen[s.Ref] = true
		}
		for _, ps := range s.Properties {
			walk(ps)
		}
		walk(s.Items)
		walk(s.Additional)
	}
	walk(s)
	return sortedKeys(seen)
}

// inlinedSize counts s's nodes with refs inlined, stopping past
// maxSchemaNodes. Shared nodes are counted once per use, but computed once.
func (s *Schema) inlinedSize() int {
	memo := map[*Schema]int{}
	var size func(*Schema) int
	size = func(s *Schema) int {
		s = s.deref()
		if s == nil {
			return 0
		}
		if n, ok := memo[s]; ok {
			return n
		}
		n := 1
		for _, ps := range s.Properties {
			n += size(ps)
		}
		n += size(s.Items) + size(s.Additional)
		n = min(n, maxSchemaNodes+1)
		memo[s] = n
		return n
	}
	return size(s)
}

// resolveSchema returns a copy of s that the definition owns, with refs
// resolved against its named schemas. s itself is never modified: it may
// be a host's, shared by every definition the host loads, so writing into
// it would race and would let one definition's names leak into another's.
// Sharing within s is kept, so the copy is no larger than s.
func (d *Definition) resolveSchema(p *problems, where, key string, s *Schema) *Schema {
	at := ""
	if key != "" {
		at = key + ": "
	}
	copies := map[*Schema]*Schema{}
	onPath := map[*Schema]bool{}
	var walk func(*Schema) *Schema
	walk = func(s *Schema) *Schema {
		if s == nil {
			return nil
		}
		if onPath[s] {
			p.add(where, "%sschema contains itself; recursive schemas are not supported", at)
			return nil
		}
		if c, ok := copies[s]; ok {
			return c
		}
		onPath[s] = true
		defer delete(onPath, s)
		c := &Schema{
			Type: s.Type, Description: s.Description, Required: s.Required, Enum: s.Enum,
			Default: s.Default, AdditionalSet: s.AdditionalSet, Ref: s.Ref,
		}
		if len(s.Properties) > 0 {
			c.Properties = make(map[string]*Schema, len(s.Properties))
			for _, name := range sortedKeys(s.Properties) {
				c.Properties[name] = walk(s.Properties[name])
			}
		}
		c.Items = walk(s.Items)
		c.Additional = walk(s.Additional)
		if s.Ref != "" {
			c.resolved = d.Schemas[s.Ref]
			if c.resolved == nil {
				p.add(where, "%sunknown schema %q", at, s.Ref)
			}
		}
		copies[s] = c
		return c
	}
	out := walk(s)
	if out.inlinedSize() > maxSchemaNodes {
		p.add(where, "%sschema has more than %d nodes with its refs inlined", at, maxSchemaNodes)
	}
	return out
}

// deref returns the schema a ref points to: nil for a ref that is not
// resolved (unknown, cyclic, or a schema no definition resolved). Refs in
// a loaded definition never form a cycle, so this terminates, and so does
// every walk over a schema's structure.
func (s *Schema) deref() *Schema {
	for s != nil && s.Ref != "" {
		s = s.resolved
	}
	return s
}

func (s *Schema) allows(typ string) bool {
	if len(s.Type) == 0 {
		return true
	}
	for _, t := range s.Type {
		if t == typ || (typ == "integer" && t == "number") {
			return true
		}
	}
	return false
}

func (s *Schema) isRequired(name string) bool { return slices.Contains(s.Required, name) }

// defaultValue returns the schema's default, looking through refs: a
// property may carry its own default or name a schema that has one.
func (s *Schema) defaultValue() any {
	for ; s != nil; s = s.resolved {
		if s.Default != nil {
			return s.Default
		}
		if s.Ref == "" {
			break
		}
	}
	return nil
}

// checkDefaults reports every default in s that does not validate against
// the schema it belongs to. It does not follow refs: named schemas are
// checked where they are declared.
func (s *Schema) checkDefaults(p *problems, where, key string) {
	seen := map[*Schema]bool{}
	var walk func(at string, s *Schema)
	walk = func(at string, s *Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		if s.Default != nil {
			for _, msg := range s.validate(s.Default) {
				p.add(where, "%s.default: %s", at, strings.TrimPrefix(msg, "value: "))
			}
		}
		for _, name := range sortedKeys(s.Properties) {
			walk(at+"."+name, s.Properties[name])
		}
		walk(at+"[]", s.Items)
		walk(at+".*", s.Additional)
	}
	walk(key, s)
}

// exprType converts the schema to an expr type for static checking. The
// mapping is the table in documentation/definitions.md:
//
//   - a schema that says nothing about its kind (`{}`, `true`, only
//     properties or a description) is Any: untyped, like a step with no
//     schema, and outside the promise;
//   - an enum is the union of its values' literal types, restricted to the
//     values `type` allows;
//   - otherwise each listed type maps to its kind and the kinds are joined,
//     so `["string", "number"]` is a union that operations needing one
//     kind reject, and `null` makes the type nullable;
//   - an object with no properties and no additionalProperties is an open
//     object: it is checked for kind and nil, its fields are untyped;
//   - an object with properties is closed unless additionalProperties is
//     true or a schema, whose type is the type of every other field.
//
// A property not in required and without a default may be nil. exprType
// and validate agree on nil: nullable reports exactly when the type admits
// nil.
func (s *Schema) exprType() *expr.Type { return schemaTypes{}.of(s) }

// schemaTypes converts schemas to types, once per schema node, so a schema
// whose refs are shared converts in time linear in its size. The types it
// returns are shared and must not be modified.
type schemaTypes map[*Schema]*expr.Type

func (m schemaTypes) of(s *Schema) *expr.Type {
	s = s.deref()
	if s == nil {
		return expr.Any
	}
	if t, ok := m[s]; ok {
		return t
	}
	t := m.convert(s)
	m[s] = t
	return t
}

func (m schemaTypes) convert(s *Schema) *expr.Type {
	if len(s.Enum) > 0 {
		var t *expr.Type
		for _, v := range s.enumValues() {
			t = expr.Join(t, literalType(v))
		}
		if t == nil {
			return expr.Any // no value is valid, so no expression reads one
		}
		return t
	}
	if len(s.Type) == 0 {
		return expr.Any
	}
	var t *expr.Type
	nullable := false
	for _, k := range s.Type {
		switch k {
		case "null":
			nullable = true
		case "string":
			t = expr.Join(t, expr.String())
		case "number", "integer":
			t = expr.Join(t, expr.Number())
		case "boolean":
			t = expr.Join(t, expr.Bool())
		case "array":
			t = expr.Join(t, expr.ListOf(m.of(s.Items)))
		case "object":
			t = expr.Join(t, m.object(s))
		}
	}
	switch {
	case t == nil:
		return expr.Null()
	case nullable:
		return t.OrNil()
	}
	return t
}

func (m schemaTypes) object(s *Schema) *expr.Type {
	if len(s.Properties) == 0 && !s.AdditionalSet {
		return expr.OpenObject(nil) // shape unknown: fields are untyped
	}
	fields := make(map[string]*expr.Type, len(s.Properties))
	for name, ps := range s.Properties {
		ft := m.of(ps)
		if !s.isRequired(name) && ps.defaultValue() == nil {
			ft = ft.OrNil()
		}
		fields[name] = ft
	}
	t := expr.ObjectOf(fields)
	if s.Additional != nil {
		t.Open = true
		t.Elem = m.of(s.Additional)
	}
	return t
}

// enumValues are the enum values the schema's type allows: a value must be
// in the enum and of a listed type.
func (s *Schema) enumValues() []any {
	var out []any
	for _, v := range s.Enum {
		if s.allows(jsonType(v)) {
			out = append(out, v)
		}
	}
	return out
}

// validate checks a JSON-normalized value against the schema and returns
// one message per problem, at most ten. Messages name the value "value".
func (s *Schema) validate(v any) []string { return s.validateAs(v, "") }

// validateAs is validate with the value named root in messages.
func (s *Schema) validateAs(v any, root string) []string {
	var out []string
	s.validateAt(v, root, &out)
	if len(out) > 10 {
		out = append(out[:10], fmt.Sprintf("and %d more", len(out)-10))
	}
	return out
}

func (s *Schema) validateAt(v any, path string, out *[]string) {
	s = s.deref()
	if s == nil {
		return
	}
	at := path
	if at == "" {
		at = "value"
	}
	if len(s.Enum) > 0 {
		found := false
		for _, e := range s.Enum {
			if jsonEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			*out = append(*out, fmt.Sprintf("%s: %s is not one of %s", at, describe(v), formatEnum(s.Enum)))
			return
		}
	}
	typ := jsonType(v)
	if !s.allows(typ) {
		*out = append(*out, fmt.Sprintf("%s: expected %s, got %s", at, strings.Join(s.Type, " or "), typ))
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for _, name := range s.Required {
			ps, declared := s.Properties[name]
			if pv, ok := t[name]; !ok || pv == nil && !(declared && ps.nullable()) {
				*out = append(*out, fmt.Sprintf("%s: missing required property %q", at, name))
			}
		}
		for _, name := range sortedKeys(t) {
			pv := t[name]
			child := join(path, name)
			if ps, ok := s.Properties[name]; ok {
				if pv == nil && !s.isRequired(name) {
					continue // an optional property may be nil
				}
				ps.validateAt(pv, child, out)
			} else if s.Additional != nil {
				s.Additional.validateAt(pv, child, out)
			} else if s.AdditionalSet {
				*out = append(*out, fmt.Sprintf("%s: unexpected property %q", at, name))
			}
		}
	case []any:
		if s.Items != nil {
			for i, item := range t {
				s.Items.validateAt(item, fmt.Sprintf("%s[%d]", at, i), out)
			}
		}
	}
}

// complete returns v with every declared property it lacks, or holds as
// nil, set to the property's default, and every other absent property set
// to nil, so that reading an optional property that is absent gives nil, as
// its type says, rather than a missing-key error. It walks every value a
// schema types: properties, items, and the values of undeclared properties
// under additionalProperties. v must already be valid.
func (s *Schema) complete(v any) any {
	s = s.deref()
	if s == nil {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		if len(s.Properties) == 0 && s.Additional == nil {
			return v
		}
		out := make(map[string]any, len(t)+len(s.Properties))
		for k, pv := range t {
			if ps, ok := s.Properties[k]; ok {
				pv = ps.complete(pv)
			} else if s.Additional != nil {
				pv = s.Additional.complete(pv)
			}
			out[k] = pv
		}
		for name, ps := range s.Properties {
			if out[name] != nil {
				continue
			}
			out[name] = nil
			if d := ps.defaultValue(); d != nil {
				// A copy, so the state never shares a value with the
				// definition.
				d, _, _ = normalize(d)
				out[name] = ps.complete(d)
			}
		}
		return out
	case []any:
		if s.Items == nil {
			return v
		}
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = s.Items.complete(item)
		}
		return out
	}
	return v
}

// nullable reports whether nil is a valid value, exactly when exprType
// admits nil: a null type, null in the enum (and allowed by the type), or
// an untyped schema.
func (s *Schema) nullable() bool {
	s = s.deref()
	if s == nil {
		return true
	}
	if len(s.Enum) > 0 {
		for _, v := range s.enumValues() {
			if v == nil {
				return true
			}
		}
		return false
	}
	return len(s.Type) == 0 || slices.Contains(s.Type, "null")
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func jsonType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		// Exact for every float64: no conversion to int64, which saturates
		// or overflows past 2^63.
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func jsonEqual(a, b any) bool {
	ad, _ := json.Marshal(a)
	bd, _ := json.Marshal(b)
	return string(ad) == string(bd)
}

func describe(v any) string {
	data, _ := json.Marshal(v)
	if len(data) > 40 {
		return string(data[:37]) + "..."
	}
	return string(data)
}

func formatEnum(values []any) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = describe(v)
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}

// UnmarshalJSON reads a JSON Schema document, so commands that carry an
// output schema survive a round trip through a host's outbox.
func (s *Schema) UnmarshalJSON(data []byte) error {
	parsed, err := ParseSchema(data)
	if err != nil {
		return err
	}
	*s = *parsed
	return nil
}

// MarshalJSON writes the schema as JSON Schema, with refs inlined, so a
// host can hand it to a model as a structured-output schema. Load rejects
// recursive refs and bounds the inlined size (maxSchemaNodes), so the
// result is finite and small, and it parses back to the same schema.
func (s *Schema) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.jsonValue())
}

func (s *Schema) jsonValue() any {
	def := s.defaultValue()
	s = s.deref()
	if s == nil {
		return map[string]any{}
	}
	m := map[string]any{}
	switch len(s.Type) {
	case 0:
	case 1:
		m["type"] = s.Type[0]
	default:
		m["type"] = s.Type
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for name, ps := range s.Properties {
			props[name] = ps.jsonValue()
		}
		m["properties"] = props
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Items != nil {
		m["items"] = s.Items.jsonValue()
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if def != nil {
		m["default"] = def
	}
	if s.AdditionalSet || s.Additional != nil {
		if s.Additional == nil {
			m["additionalProperties"] = false
		} else {
			m["additionalProperties"] = s.Additional.jsonValue()
		}
	}
	return m
}
