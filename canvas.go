package workflow

import "fmt"

// Canvas is what a visual editor draws around the steps: boxes that group
// them and free notes. It does not change how a run behaves; Apply never
// reads it. Coordinates are in whatever units the editor uses, as in a
// step's [Layout].
type Canvas struct {
	Groups []*Group
	Notes  []*Note
}

// Group is a named box around a set of steps, which an editor may
// collapse into one node.
type Group struct {
	// Name is an identifier, unique among groups and distinct from every
	// step name.
	Name        string
	Description string
	// Steps are the names of the steps the group contains, from steps,
	// on_failure or finally. A step belongs to at most one group.
	Steps []string
	// Parent is the name of the group this one sits inside, or "". A
	// parent has no parent of its own: groups nest one level deep.
	Parent string
	// Layout is where the box is drawn. Nil when the editor has not
	// saved one.
	Layout *GroupLayout
}

// GroupLayout is a group's box and whether it is drawn collapsed by
// default.
type GroupLayout struct {
	X         float64
	Y         float64
	Width     float64
	Height    float64
	Collapsed bool
}

// Note is free text on the canvas. Text may be Markdown.
type Note struct {
	Text string
	X    float64
	Y    float64
	// Width and Height are the note's size, or 0 to let the editor size
	// it.
	Width  float64
	Height float64
	// Attach is the name of the step or group the note moves with, or
	// "". X and Y are then an offset from that node's position.
	Attach string
}

// parseCanvas reads `canvas: {groups: [...], notes: [...]}`. Names are
// checked against the steps in checkCanvas.
func parseCanvas(p *problems, v any) *Canvas {
	m := p.obj("", "canvas", v)
	if m == nil {
		return nil
	}
	c := &Canvas{}
	for _, k := range sortedKeys(m) {
		switch k {
		case "groups":
			c.Groups = parseList(p, "canvas.groups", m[k], parseGroup)
		case "notes":
			c.Notes = parseList(p, "canvas.notes", m[k], parseNote)
		default:
			p.add("", "canvas: unknown key %q (it has groups and notes)", k)
		}
	}
	return c
}

// parseList reads a list of objects with parse, which gets each entry's
// position as `key[i]`.
func parseList[T any](p *problems, key string, v any,
	parse func(*problems, string, map[string]any) *T) []*T {
	list, ok := v.([]any)
	if !ok {
		p.add("", "%s must be a list", key)
		return nil
	}
	out := make([]*T, 0, len(list))
	for i, raw := range list {
		at := fmt.Sprintf("%s[%d]", key, i)
		m := p.obj("", at, raw)
		if m == nil {
			continue
		}
		out = append(out, parse(p, at, m))
	}
	return out
}

func parseGroup(p *problems, at string, m map[string]any) *Group {
	g := &Group{}
	if v, ok := m["name"]; !ok || v == "" {
		p.add("", "%s needs a name", at)
	}
	for _, k := range sortedKeys(m) {
		v := m[k]
		switch k {
		case "name":
			g.Name = p.str("", at+".name", v)
		case "description":
			g.Description = p.str("", at+".description", v)
		case "steps":
			g.Steps = p.strList("", at+".steps", v)
		case "parent":
			g.Parent = p.str("", at+".parent", v)
		case "layout":
			g.Layout = parseGroupLayout(p, at+".layout", v)
		default:
			p.add("", "%s: unknown key %q", at, k)
		}
	}
	return g
}

func parseGroupLayout(p *problems, at string, v any) *GroupLayout {
	m := p.obj("", at, v)
	if m == nil {
		return nil
	}
	l := &GroupLayout{
		X:      number(p, at, m, "x", true),
		Y:      number(p, at, m, "y", true),
		Width:  size(p, at, m, "width", true),
		Height: size(p, at, m, "height", true),
	}
	if c, ok := m["collapsed"]; ok {
		l.Collapsed = p.boolean("", at+".collapsed", c)
	}
	for _, k := range sortedKeys(m) {
		switch k {
		case "x", "y", "width", "height", "collapsed":
		default:
			p.add("", "%s: unknown key %q", at, k)
		}
	}
	return l
}

func parseNote(p *problems, at string, m map[string]any) *Note {
	n := &Note{
		X:      number(p, at, m, "x", true),
		Y:      number(p, at, m, "y", true),
		Width:  size(p, at, m, "width", false),
		Height: size(p, at, m, "height", false),
	}
	if v, ok := m["text"]; !ok || v == "" {
		p.add("", "%s needs text", at)
	} else {
		n.Text = p.str("", at+".text", v)
	}
	if v, ok := m["attach"]; ok {
		n.Attach = p.str("", at+".attach", v)
	}
	for _, k := range sortedKeys(m) {
		switch k {
		case "text", "x", "y", "width", "height", "attach":
		default:
			p.add("", "%s: unknown key %q", at, k)
		}
	}
	return n
}

// number reads m[k] as a number, reporting it missing when required.
func number(p *problems, at string, m map[string]any, k string, required bool) float64 {
	v, ok := m[k]
	if !ok {
		if required {
			p.add("", "%s needs %s", at, k)
		}
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		p.add("", "%s.%s must be a number", at, k)
	}
	return f
}

// size reads m[k] as a number greater than zero.
func size(p *problems, at string, m map[string]any, k string, required bool) float64 {
	f := number(p, at, m, k, required)
	if _, isNum := m[k].(float64); isNum && f <= 0 {
		p.add("", "%s.%s must be greater than 0", at, k)
	}
	return f
}

// checkCanvas checks the canvas's names against the steps: group names,
// the steps each group contains, parents and note attachments.
func (d *Definition) checkCanvas(p *problems) {
	if d.Canvas == nil {
		return
	}
	groups := map[string]*Group{}
	for i, g := range d.Canvas.Groups {
		at := fmt.Sprintf("canvas.groups[%d]", i)
		switch {
		case g.Name == "":
			continue // reported by parseGroup
		case !identRE.MatchString(g.Name):
			p.add("", "%s: name %q must be an identifier (letters, digits, underscore)",
				at, g.Name)
		case d.byName[g.Name] != nil:
			p.add("", "%s: name %q is a step's name", at, g.Name)
		case groups[g.Name] != nil:
			p.add("", "%s: duplicate group name %q", at, g.Name)
			continue
		}
		groups[g.Name] = g
	}

	owner := map[string]string{}
	for i, g := range d.Canvas.Groups {
		at := fmt.Sprintf("canvas.groups[%d]", i)
		if g.Name != "" {
			at = fmt.Sprintf("canvas: group %q", g.Name)
		}
		for _, s := range g.Steps {
			switch {
			case d.byName[s] == nil:
				p.add("", "%s: unknown step %q", at, s)
			case owner[s] == g.Name:
				p.add("", "%s: step %q appears twice", at, s)
			case owner[s] != "":
				p.add("", "%s: step %q is already in group %q", at, s, owner[s])
			default:
				owner[s] = g.Name
			}
		}
		if g.Parent == "" {
			continue
		}
		parent := groups[g.Parent]
		switch {
		case parent == nil:
			p.add("", "%s: parent: unknown group %q", at, g.Parent)
		case parent == g:
			p.add("", "%s: parent: a group cannot be its own parent", at)
		case parent.Parent != "":
			p.add("", "%s: parent: group %q is inside %q itself; groups nest one level deep",
				at, g.Parent, parent.Parent)
		}
	}

	for i, n := range d.Canvas.Notes {
		if n.Attach != "" && d.byName[n.Attach] == nil && groups[n.Attach] == nil {
			p.add("", "canvas.notes[%d]: attach: no step or group named %q", i, n.Attach)
		}
	}
}
