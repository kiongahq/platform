// Package agentgraph implements the native Agent Designer graph contract
// (kiongahq/contracts agent-graph): the component registry, version 1 to 2
// migration, typed validation, starter templates and the canonical behavior
// hash. The gateway validates and hashes graphs here; agent-runtime executes
// them. Neither side evaluates code from a graph.
package agentgraph

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
)

//go:embed components.v1.json templates/*.json
var contractFiles embed.FS

// Port is an input or output of a component.
type Port struct {
	Name     string   `json:"name"`
	Type     string   `json:"type,omitempty"`
	Accepts  []string `json:"accepts,omitempty"`
	Required bool     `json:"required,omitempty"`
}

// Field is one configuration setting of a component.
type Field struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Type      string   `json:"type"`
	Resource  string   `json:"resource,omitempty"`
	Required  bool     `json:"required"`
	Default   string   `json:"default,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
	Minimum   *int     `json:"minimum,omitempty"`
	Maximum   *int     `json:"maximum,omitempty"`
	Options   []string `json:"options,omitempty"`
	Help      string   `json:"help,omitempty"`
}

// Component is a registered node kind.
type Component struct {
	Kind           string  `json:"kind"`
	Version        string  `json:"version"`
	Category       string  `json:"category"`
	Label          string  `json:"label"`
	Description    string  `json:"description"`
	Inputs         []Port  `json:"inputs"`
	Outputs        []Port  `json:"outputs"`
	Config         []Field `json:"config"`
	SideEffects    bool    `json:"side_effects"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	Retries        int     `json:"retries"`
	Singleton      bool    `json:"singleton,omitempty"`
}

// Conversion is a permitted implicit port type conversion.
type Conversion struct {
	From string `json:"from"`
	To   string `json:"to"`
	Rule string `json:"rule"`
}

// Operator is a condition operator.
type Operator struct {
	Op          string `json:"op"`
	Argument    bool   `json:"argument"`
	Description string `json:"description"`
}

// Registry is the parsed components.v1.json.
type Registry struct {
	Version            string            `json:"registry_version"`
	PortTypes          map[string]string `json:"port_types"`
	Conversions        []Conversion      `json:"conversions"`
	ConditionOperators []Operator        `json:"condition_operators"`
	TemplateVariables  map[string]string `json:"template_variables"`
	Components         []Component       `json:"components"`
	byKind             map[string]*Component
}

// Template is a starter graph.
type Template struct {
	ID              string `json:"id"`
	TemplateVersion string `json:"template_version"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Graph           Graph  `json:"graph"`
}

var (
	defaultOnce      sync.Once
	defaultRegistry  *Registry
	defaultTemplates []Template
	defaultErr       error
)

// Default returns the embedded registry. It panics only if the embedded
// contract is malformed, which the package tests rule out.
func Default() *Registry {
	load()
	if defaultErr != nil {
		panic(defaultErr)
	}
	return defaultRegistry
}

// Templates returns the embedded starter templates sorted by ID.
func Templates() []Template {
	load()
	return slices.Clone(defaultTemplates)
}

func load() {
	defaultOnce.Do(func() {
		raw, err := contractFiles.ReadFile("components.v1.json")
		if err != nil {
			defaultErr = err
			return
		}
		defaultRegistry, defaultErr = ParseRegistry(raw)
		if defaultErr != nil {
			return
		}
		entries, err := contractFiles.ReadDir("templates")
		if err != nil {
			defaultErr = err
			return
		}
		for _, entry := range entries {
			data, err := contractFiles.ReadFile("templates/" + entry.Name())
			if err != nil {
				defaultErr = err
				return
			}
			var template Template
			if err := json.Unmarshal(data, &template); err != nil {
				defaultErr = fmt.Errorf("template %s: %w", entry.Name(), err)
				return
			}
			defaultTemplates = append(defaultTemplates, template)
		}
		sort.Slice(defaultTemplates, func(i, j int) bool { return defaultTemplates[i].ID < defaultTemplates[j].ID })
	})
}

// ParseRegistry decodes and indexes a registry document.
func ParseRegistry(raw []byte) (*Registry, error) {
	var registry Registry
	if err := json.Unmarshal(raw, &registry); err != nil {
		return nil, fmt.Errorf("component registry: %w", err)
	}
	registry.byKind = map[string]*Component{}
	for index := range registry.Components {
		component := &registry.Components[index]
		if registry.byKind[component.Kind] != nil {
			return nil, fmt.Errorf("component registry: duplicate kind %q", component.Kind)
		}
		registry.byKind[component.Kind] = component
	}
	return &registry, nil
}

// Component returns the component for kind, or nil.
func (r *Registry) Component(kind string) *Component {
	return r.byKind[kind]
}

// Compatible reports whether an output of type from may feed an input that
// accepts the listed types, directly or through a registered conversion.
func (r *Registry) Compatible(from string, accepts []string) bool {
	if slices.Contains(accepts, from) {
		return true
	}
	for _, conversion := range r.Conversions {
		if conversion.From == from && slices.Contains(accepts, conversion.To) {
			return true
		}
	}
	return false
}

func (c *Component) output(name string) *Port {
	for index := range c.Outputs {
		if c.Outputs[index].Name == name {
			return &c.Outputs[index]
		}
	}
	return nil
}

func (c *Component) input(name string) *Port {
	for index := range c.Inputs {
		if c.Inputs[index].Name == name {
			return &c.Inputs[index]
		}
	}
	return nil
}

func (c *Component) field(key string) *Field {
	for index := range c.Config {
		if c.Config[index].Key == key {
			return &c.Config[index]
		}
	}
	return nil
}
