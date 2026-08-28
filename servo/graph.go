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
	// Scope names the scope this node belongs to, by key type, and is
	// empty for an ordinary singleton. For a scoped node, Level counts
	// from its own scope's floor rather than the app's, since a scope's
	// Init phases don't depend on how deep the singletons it borrows
	// happen to be.
	//
	// Omitted from JSON when empty, so a graph with no scopes serializes
	// byte for byte the way it did before scopes existed.
	Scope string `json:"scope,omitempty"`
}

// GraphScope is one declared scope: its key type, its policy, the
// accessor interfaces that expose it, and every node one instance of it
// holds.
type GraphScope struct {
	Key    string `json:"key"`
	Linger string `json:"linger"`
	Max    int    `json:"max"`
	// Accessors are the user-declared interfaces the generated accessors
	// satisfy, in declaration order.
	Accessors []string `json:"accessors"`
	// Members is every node constructed once per live key, in
	// construction order.
	Members []string `json:"members"`
	// Borrows is every singleton the scope's members depend on. Those are
	// constructed once by the App and shared by every instance, which is
	// why they are listed separately rather than counted as members.
	Borrows []string `json:"borrows"`
}

// Graph is the full resolved object graph, serializable to the same JSON
// schema `servo graph --format=json` produces at build time.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	// Scopes is omitted entirely when nothing is scoped.
	Scopes []GraphScope `json:"scopes,omitempty"`
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
