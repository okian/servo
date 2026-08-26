package servo

import "time"

// GraphNode is one resolved node, emitted as a compile-time constant by the
// generated App.Graph(). It is display-only: type strings are labels,
// never lookup keys, and there is no path from a GraphNode back to the
// instance it describes.
type GraphNode struct {
	Type         string   `json:"type"`
	Level        int      `json:"level"`
	Deps         []string `json:"deps"`
	Capabilities []string `json:"capabilities"`
	Binding      string   `json:"binding"`
	Pos          string   `json:"pos"`
}

// Graph is the full resolved object graph, serializable to the same JSON
// schema `servo graph --format=json` produces at build time.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
}

// StartupNode is one node's construction/Init timing.
type StartupNode struct {
	Type     string        `json:"type"`
	Duration time.Duration `json:"duration"`
}

// StartupReport breaks down startup cost per node without external
// instrumentation.
type StartupReport struct {
	Nodes []StartupNode `json:"nodes"`
}
