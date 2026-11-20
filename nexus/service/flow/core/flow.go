package flow

import (
	"flame/nexus/shared/models"
)

type Flow struct {
	Instance     *models.Flow
	Nodes        []*Node                `json:"nodes"`
	NodeMap      map[string]*Node       `json:"-"`
	Edges        []*Edge                `json:"edges"`
	DefEnvVars   map[string]interface{} `json:"env_vars"`
	DefFlowVars  map[string]interface{} `json:"flow_vars"`
	DefArguments map[string]interface{} `json:"arguments"`
}

type Node struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Data       *NodeData `json:"data"`
	Position   Position  `json:"position"`
	ParentNode string    `json:"parent_node,omitempty"`
	InEdges    []*Edge   // upstream: normally exactly one, multi-upstream is reserved for later
	OutEdges   []*Edge   // downstream: several are allowed, branch nodes use more than one
}

// Label returns the display name of the node: data.label is the only source, with the node id as fallback.
func (n *Node) Label() string {
	if n == nil {
		return ""
	}
	if n.Data != nil && n.Data.Label != "" {
		return n.Data.Label
	}
	return n.ID
}

type NodeData struct {
	Label  string                 `json:"label"`
	Config map[string]interface{} `json:"config"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHandle string `json:"source_handle,omitempty"`
	TargetHandle string `json:"target_handle,omitempty"`
}

type EnvVar struct {
	Type        string `json:"type"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

type FlowVar struct {
	Type        string                 `json:"type"`
	Default     string                 `json:"default"`
	Description string                 `json:"description"`
	Value       interface{}            `json:"value"`
	Properties  map[string]interface{} `json:"properties,omitempty"`
}

type LocalVar struct {
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Value       interface{} `json:"value"`
}
type LoopVar struct {
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Value       interface{} `json:"value"`
}
