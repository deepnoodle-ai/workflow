package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

// Schema is the subset of JSON Schema the library uses to type step
// outputs, step parameters, inputs and the trigger event.
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
type Schema struct {
	Type        []string           // string, number, integer, boolean, object, array, null
	Description string             //
	Properties  map[string]*Schema //
	Required    []string           //
	Items       *Schema            //
	Enum        []any              //
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
		case "$ref":
			s.Ref = p.str(where, at, val)
		default:
			p.add(where, "%s: unknown schema key %q", key, k)
		}
	}
	return s
}

// resolveNamedSchemas resolves the refs inside the definition's own
// `schemas:`, in place: Load parsed them, so the definition owns them.
func (d *Definition) resolveNamedSchemas(p *problems) {
	seen := map[*Schema]bool{}
	var walk func(where string, s *Schema)
	walk = func(where string, s *Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		if s.Ref != "" {
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
}

// resolveSchema returns a copy of s that the definition owns, with refs
// resolved against its named schemas. s itself is never modified: it may
// be a host's, shared by every definition the host loads, so writing into
// it would race and would let one definition's names leak into another's.
// Sharing within s is kept, so the copy is no larger than s.
func (d *Definition) resolveSchema(p *problems, where string, s *Schema) *Schema {
	copies := map[*Schema]*Schema{}
	onPath := map[*Schema]bool{}
	var walk func(*Schema) *Schema
	walk = func(s *Schema) *Schema {
		if s == nil {
			return nil
		}
		if onPath[s] {
			p.add(where, "schema contains itself; recursive schemas are not supported")
			return nil
		}
		if c, ok := copies[s]; ok {
			return c
		}
		onPath[s] = true
		defer delete(onPath, s)
		c := &Schema{
			Type: s.Type, Description: s.Description, Required: s.Required, Enum: s.Enum,
			AdditionalSet: s.AdditionalSet, Ref: s.Ref,
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
				p.add(where, "unknown schema %q", s.Ref)
			}
		}
		copies[s] = c
		return c
	}
	return walk(s)
}

// deref returns the schema a ref points to.
func (s *Schema) deref() *Schema {
	for i := 0; s != nil && s.Ref != "" && i < 32; i++ {
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

func (s *Schema) isRequired(name string) bool { return contains(s.Required, name) }

// exprType converts the schema to an expr type for static checking.
func (s *Schema) exprType() *expr.Type { return s.exprTypeDepth(0) }

func (s *Schema) exprTypeDepth(depth int) *expr.Type {
	s = s.deref()
	if s == nil || depth > 16 {
		return expr.Any
	}
	nullable := false
	var kinds []string
	for _, t := range s.Type {
		if t == "null" {
			nullable = true
		} else {
			kinds = append(kinds, t)
		}
	}
	if len(kinds) == 0 && len(s.Enum) > 0 {
		kinds = enumKinds(s.Enum)
	}
	for _, e := range s.Enum {
		if e == nil {
			nullable = true
		}
	}
	var t *expr.Type
	switch {
	case len(kinds) == 0 && nullable:
		return expr.Null()
	case len(kinds) != 1:
		t = expr.Any
	default:
		switch kinds[0] {
		case "string":
			t = expr.String()
		case "number", "integer":
			t = expr.Number()
		case "boolean":
			t = expr.Bool()
		case "array":
			t = expr.ListOf(s.Items.exprTypeDepth(depth + 1))
		case "object":
			t = s.objectType(depth)
		}
	}
	if len(s.Enum) > 0 && t.Kind != expr.KindAny {
		for _, e := range s.Enum {
			if e != nil {
				t.Enum = append(t.Enum, e)
			}
		}
	}
	if nullable {
		t = t.OrNil()
	}
	return t
}

func (s *Schema) objectType(depth int) *expr.Type {
	if len(s.Properties) == 0 && s.Additional == nil {
		return expr.Any // an object of unknown shape is unchecked
	}
	fields := make(map[string]*expr.Type, len(s.Properties))
	for name, ps := range s.Properties {
		ft := ps.exprTypeDepth(depth + 1)
		if !s.isRequired(name) {
			ft = ft.OrNil()
		}
		fields[name] = ft
	}
	t := expr.ObjectOf(fields)
	if s.Additional != nil {
		t.Open = true
		t.Elem = s.Additional.exprTypeDepth(depth + 1)
	}
	return t
}

func enumKinds(values []any) []string {
	seen := map[string]bool{}
	for _, v := range values {
		switch v.(type) {
		case string:
			seen["string"] = true
		case float64:
			seen["number"] = true
		case bool:
			seen["boolean"] = true
		case nil:
		default:
			seen["object"] = true
		}
	}
	return sortedKeys(seen)
}

// validate checks a JSON-normalized value against the schema and returns
// one message per problem, at most ten.
func (s *Schema) validate(v any) []string {
	var out []string
	s.validateAt(v, "", &out, 0)
	if len(out) > 10 {
		out = append(out[:10], fmt.Sprintf("and %d more", len(out)-10))
	}
	return out
}

func (s *Schema) validateAt(v any, path string, out *[]string, depth int) {
	s = s.deref()
	if s == nil || depth > 32 {
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
			if pv, ok := t[name]; !ok || pv == nil && !s.Properties[name].nullable() {
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
				ps.validateAt(pv, child, out, depth+1)
			} else if s.Additional != nil {
				s.Additional.validateAt(pv, child, out, depth+1)
			} else if s.AdditionalSet {
				*out = append(*out, fmt.Sprintf("%s: unexpected property %q", at, name))
			}
		}
	case []any:
		if s.Items != nil {
			for i, item := range t {
				s.Items.validateAt(item, fmt.Sprintf("%s[%d]", at, i), out, depth+1)
			}
		}
	}
}

// complete returns v with every declared property it lacks set to nil, so
// that reading an optional property that is absent gives nil, as its type
// says, rather than a missing-key error. v must already be valid.
func (s *Schema) complete(v any) any { return s.completeDepth(v, 0) }

func (s *Schema) completeDepth(v any, depth int) any {
	s = s.deref()
	if s == nil || depth > 32 {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		if len(s.Properties) == 0 {
			return v
		}
		out := make(map[string]any, len(t)+len(s.Properties))
		for k, pv := range t {
			if ps, ok := s.Properties[k]; ok {
				pv = ps.completeDepth(pv, depth+1)
			}
			out[k] = pv
		}
		for name := range s.Properties {
			if _, ok := out[name]; !ok {
				out[name] = nil
			}
		}
		return out
	case []any:
		if s.Items == nil {
			return v
		}
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = s.Items.completeDepth(item, depth+1)
		}
		return out
	}
	return v
}

func (s *Schema) nullable() bool {
	s = s.deref()
	return s != nil && len(s.Type) > 0 && contains(s.Type, "null")
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
		if t == float64(int64(t)) {
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
	sort.Strings(parts)
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
// host can hand it to a model as a structured-output schema.
func (s *Schema) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.jsonValue(0))
}

func (s *Schema) jsonValue(depth int) any {
	s = s.deref()
	if s == nil || depth > 32 {
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
			props[name] = ps.jsonValue(depth + 1)
		}
		m["properties"] = props
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Items != nil {
		m["items"] = s.Items.jsonValue(depth + 1)
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if s.AdditionalSet || s.Additional != nil {
		if s.Additional == nil {
			m["additionalProperties"] = false
		} else {
			m["additionalProperties"] = s.Additional.jsonValue(depth + 1)
		}
	}
	return m
}
