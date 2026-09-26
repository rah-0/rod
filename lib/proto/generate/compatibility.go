package main

import (
	"encoding/json"
	"fmt"
	"slices"
)

// compatibilityOutput records the members that browsers older than the schema
// omit. Generation reads the record from the output directory and writes it back.
const compatibilityOutput = "generate/schema-compatibility.json"

// compatibility is the content of compatibilityOutput. Members are
// "Domain.name.member" keys, where name is a type, an event, or a command name
// followed by "Result", as for proto.GetType.
type compatibility struct {
	Members []string `json:"members"`
}

// update returns the record for domains: the recorded members, and the members
// that each older schema lacks or marks optional in a definition it has. Members
// that domains no longer requires are dropped.
func (c compatibility) update(domains []*domain, older ...[]byte) (compatibility, error) {
	current := members(domains)
	var next compatibility
	for _, key := range c.Members {
		if _, found := slices.BinarySearch(current, key); found {
			next.Members = append(next.Members, key)
		}
	}
	for _, schema := range older {
		previous, err := schemaMembers(schema)
		if err != nil {
			return next, err
		}
		for _, key := range current {
			definition, member := splitMember(key)
			if optional, found := previous[definition][member]; previous[definition] != nil && (!found || optional) {
				next.Members = append(next.Members, key)
			}
		}
	}
	slices.Sort(next.Members)
	next.Members = slices.Compact(next.Members)
	if next.Members == nil {
		next.Members = []string{}
	}
	return next, nil
}

func (c compatibility) set() map[string]bool {
	set := map[string]bool{}
	for _, key := range c.Members {
		set[key] = true
	}
	return set
}

func (c compatibility) encode() []byte {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

func splitMember(key string) (definition, member string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '.' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// schemaMembers maps the object definitions of a protocol schema to their
// members, each with whether it is optional. Every command has a result
// definition, and every object type is a definition: a schema omits "returns"
// for a command that returns an empty object, and "properties" for an object
// type without declared members.
func schemaMembers(data []byte) (map[string]map[string]bool, error) {
	type member struct {
		Name     string `json:"name"`
		Optional bool   `json:"optional"`
	}
	type definition struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Type       string   `json:"type"`
		Properties []member `json:"properties"`
		Parameters []member `json:"parameters"`
		Returns    []member `json:"returns"`
	}
	var schema struct {
		Domains []struct {
			Domain   string       `json:"domain"`
			Types    []definition `json:"types"`
			Commands []definition `json:"commands"`
			Events   []definition `json:"events"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("decode older protocol schema: %w", err)
	}
	definitions := map[string]map[string]bool{}
	add := func(name string, list []member) {
		members := map[string]bool{}
		for _, m := range list {
			members[m.Name] = m.Optional
		}
		definitions[name] = members
	}
	for _, domain := range schema.Domains {
		for _, t := range domain.Types {
			if t.Type == "object" || t.Properties != nil {
				add(domain.Domain+"."+t.ID, t.Properties)
			}
		}
		for _, c := range domain.Commands {
			add(domain.Domain+"."+c.Name+"Result", c.Returns)
		}
		for _, e := range domain.Events {
			add(domain.Domain+"."+e.Name, e.Parameters)
		}
	}
	return definitions, nil
}
