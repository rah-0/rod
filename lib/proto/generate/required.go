package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rah-0/rod/lib/utils"
)

// decodersOutput holds the generated decodeJSON methods.
const decodersOutput = "decoders.go"

// decoders generates the decodeJSON methods that decode browser data: command
// results, events, and the objects they contain. Command parameters are only
// sent, so their structs get no method and are decoded with encoding/json.
//
// Each method decodes an object in one pass and records which required members
// it has seen. A member is required when the schema neither marks it optional,
// experimental, or deprecated, nor records it in schema-compatibility.json as a
// member that older browsers omit. Browsers older than the schema can lack
// experimental and new members, and newer ones can drop deprecated members, so
// those are decoded when present but never required.
type decoders struct {
	structs map[string]*definition // struct definitions by Go name
	named   map[string]*definition // other definitions by Go name
	// decoded holds the structs that get a decodeJSON method.
	decoded map[string]bool
}

func newDecoders(domains []*domain, older map[string]bool) *decoders {
	g := &decoders{structs: map[string]*definition{}, named: map[string]*definition{}, decoded: map[string]bool{}}
	for _, domain := range domains {
		for _, d := range domain.definitions {
			if d.skip {
				continue
			}
			if d.objType == objTypeStruct {
				g.structs[d.name] = d
				for _, prop := range d.props {
					prop.older = older[d.domain.name+"."+d.originName+"."+prop.originName]
				}
			} else {
				g.named[d.name] = d
			}
		}
	}
	var queue []string
	for _, domain := range domains {
		for _, d := range domain.definitions {
			if !d.skip && d.objType == objTypeStruct && (d.cdpType == cdpTypeEvents || d.cdpType == cdpTypeCommands && !d.command) {
				queue = append(queue, d.name)
			}
		}
	}
	for len(queue) != 0 {
		name := queue[0]
		queue = queue[1:]
		if g.decoded[name] {
			continue
		}
		g.decoded[name] = true
		for _, prop := range g.structs[name].props {
			queue = append(queue, g.refs(prop.typeName)...)
		}
	}
	return g
}

// required reports whether decoding rejects a missing prop.
func required(prop *definition) bool {
	return !prop.optional && !prop.experimental && !prop.deprecated && !prop.older
}

// underlying resolves named types to the Go type they are defined as.
func (g *decoders) underlying(typ string) string {
	for {
		switch typ {
		case "TimeSinceEpoch", "MonotonicTime":
			return "float64"
		}
		d := g.named[typ]
		if d == nil {
			return typ
		}
		typ = d.typeName
	}
}

// refs returns the structs that a value of typ can contain directly.
func (g *decoders) refs(typ string) []string {
	typ = g.underlying(typ)
	for {
		if elem, ok := strings.CutPrefix(typ, "*"); ok {
			typ = elem
		} else if elem, ok := strings.CutPrefix(typ, "[]"); ok {
			typ = g.underlying(elem)
		} else {
			break
		}
	}
	if g.structs[typ] != nil {
		return []string{typ}
	}
	return nil
}

// object returns the struct that typ points to, or "".
func (g *decoders) object(typ string) string {
	if elem, ok := strings.CutPrefix(typ, "*"); ok && g.structs[elem] != nil {
		return elem
	}
	return ""
}

// value returns the helper that decodes typ, which is not a slice other than
// []byte. The helper's type parameters can be inferred from its arguments.
func (g *decoders) value(typ string) string {
	if g.object(typ) != "" {
		return "decodeObject"
	}
	switch g.underlying(typ) {
	case "string":
		return "decodeString"
	case "int":
		return "decodeInt"
	case "float64":
		return "decodeFloat"
	case "bool":
		return "decodeBool"
	case "[]byte":
		return "decodeBytes"
	case "jsonvalue.Value":
		return "decodeValue"
	case "map[string]jsonvalue.Value":
		return "decodeMap"
	}
	panic("unsupported protocol type " + typ)
}

// list returns the helper that decodes a slice of elem as an entry of another
// slice, or "" when there is none.
func (g *decoders) list(elem string) string {
	if g.object(elem) != "" {
		return "decodeObjects"
	}
	switch g.underlying(elem) {
	case "string":
		return "decodeStrings"
	case "int":
		return "decodeInts"
	case "float64":
		return "decodeFloats"
	}
	return ""
}

// slice reports the element type when typ is a slice other than []byte.
func (g *decoders) slice(typ string) (string, bool) {
	typ = g.underlying(typ)
	if typ == "[]byte" {
		return "", false
	}
	return strings.CutPrefix(typ, "[]")
}

// function returns an instantiated helper that decodes one value of typ, for
// use as the element helper of decodeList.
func (g *decoders) function(typ string) string {
	if elem, ok := g.slice(typ); ok {
		if helper := g.list(elem); helper != "" {
			return helper + "[" + typ + "]"
		}
		panic("unsupported nested array " + typ)
	}
	if object := g.object(typ); object != "" {
		return "decodeObject[" + object + "]"
	}
	if helper := g.value(typ); helper != "decodeValue" {
		return helper + "[" + typ + "]"
	}
	return "decodeValue"
}

// call returns the statement that decodes prop into the field of m.
func (g *decoders) call(prop *definition, typ string, req bool) string {
	field := "&m." + prop.name
	if elem, ok := strings.CutPrefix(typ, "*"); ok && g.object(typ) == "" {
		// An optional number or boolean.
		return fmt.Sprintf("decodePointer(d, %s, %s)", field, g.function(elem))
	}
	if elem, ok := g.slice(typ); ok {
		// decodeList takes an unnamed slice type, which keeps the number of
		// its instantiations small. decodeSlice decodes named slice types,
		// whose name its errors report.
		helper := "decodeList"
		if g.underlying(typ) != typ {
			helper = "decodeSlice"
		}
		return fmt.Sprintf("%s(d, %s, %t, %s)", helper, field, req, g.function(elem))
	}
	return fmt.Sprintf("%s(d, %s, %t)", g.value(typ), field, req)
}

// format returns the decodeJSON method of d, or "" when d is not decoded.
func (g *decoders) format(d *definition) string {
	if d.skip || d.objType != objTypeStruct || !g.decoded[d.name] {
		return ""
	}
	var code strings.Builder
	write := func(tpl string, values ...any) {
		code.WriteString(strings.TrimSpace(utils.S(tpl, values...)) + "\n")
	}
	code.WriteString("\n")
	if len(d.props) == 0 {
		write(`
		func (m *{{.name}}) decodeJSON(d *decoder) error {
			return d.skipObject()
		}
		`, "name", d.name)
		return code.String()
	}
	var names []string
	for _, prop := range d.props {
		if required(prop) {
			names = append(names, prop.originName)
		}
	}
	if len(names) > 64 {
		panic(fmt.Sprintf("%s has %d required members, more than decodeJSON can record", d.name, len(names)))
	}
	write(`func (m *{{.name}}) decodeJSON(d *decoder) error {`, "name", d.name)
	if len(names) != 0 {
		code.WriteString("var seen uint64\n")
	}
	write(`
	ok, err := d.object()
	for ok && err == nil {
		var name []byte
		if name, ok, err = d.member(); !ok || err != nil {
			break
		}
		switch string(name) {
	`)
	bit := 0
	for _, prop := range d.props {
		req := required(prop)
		typ := prop.typeName
		if prop.optional {
			// The struct field of an optional number or boolean can be a pointer.
			typ = fieldType(d, prop)
		}
		fmt.Fprintf(&code, "case %q:\n", prop.originName)
		if req {
			fmt.Fprintf(&code, "seen |= 1 << %d\n", bit)
			bit++
		}
		fmt.Fprintf(&code, "err = inField(%s, %q)\n", g.call(prop, typ, req), prop.originName)
	}
	write(`
		default:
			err = d.skip()
		}
	}
	`)
	if len(names) == 0 {
		code.WriteString("return err\n}\n")
		return code.String()
	}
	write(`
	if err != nil {
		return err
	}
	if seen != 1<<{{.count}}-1 && !d.lenient {
		return missingMember(seen, "{{.names}}")
	}
	return nil
	}
	`, "count", len(names), "names", strings.Join(names, " "))
	return code.String()
}

// minimalJSON is the smallest JSON object of the struct name that decodes
// without an error: it has every required member, each with the smallest
// value of its type.
func (g *decoders) minimalJSON(name string) string {
	return g.minimal(name, map[string]bool{})
}

func (g *decoders) minimal(name string, visiting map[string]bool) string {
	if g.structs[name] == nil {
		panic("unknown object type " + name)
	}
	if visiting[name] {
		panic("required object cycle through " + name)
	}
	visiting[name] = true
	defer delete(visiting, name)
	var fields []string
	for _, prop := range g.structs[name].props {
		if !required(prop) {
			continue
		}
		value := ""
		if object := g.object(prop.typeName); object != "" {
			value = g.minimal(object, visiting)
		} else if _, ok := g.slice(prop.typeName); ok {
			value = "[]"
		} else {
			value = map[string]string{
				"decodeString": `""`, "decodeInt": "0", "decodeFloat": "0", "decodeBool": "false",
				"decodeBytes": `""`, "decodeValue": "null", "decodeMap": "{}",
			}[g.value(prop.typeName)]
		}
		fields = append(fields, `"`+prop.originName+`":`+value)
	}
	return "{" + strings.Join(fields, ",") + "}"
}

// members returns the "Domain.name.member" keys of the members that are not
// optional in the decoded definitions of domains.
func members(domains []*domain) []string {
	var keys []string
	for _, domain := range domains {
		for _, d := range domain.definitions {
			if d.skip || d.objType != objTypeStruct || d.command {
				continue
			}
			for _, prop := range d.props {
				if !prop.optional {
					keys = append(keys, domain.name+"."+d.originName+"."+prop.originName)
				}
			}
		}
	}
	slices.Sort(keys)
	return keys
}
