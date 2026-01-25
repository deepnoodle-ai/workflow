package domain

// JoinConfig configures a step to wait for multiple paths to converge.
type JoinConfig struct {
	// Paths specifies which named paths to wait for. If empty, waits for all active paths.
	Paths []string `json:"paths,omitempty"`

	// Count specifies the number of paths to wait for. If 0, waits for all specified paths.
	Count int `json:"count,omitempty"`

	// PathMappings specifies where to store path data. Supports two syntaxes:
	// 1. Store entire path state: "pathID": "destination"
	//    Example: "pathA": "results.pathA" stores all pathA variables under results.pathA
	// 2. Extract specific variables: "pathID.variable": "destination"
	//    Example: "pathA.result": "extracted.value" stores only pathA.result under extracted.value
	// Supports nested field extraction using dot notation for both variable names and destinations.
	PathMappings map[string]string `json:"path_mappings,omitempty"`
}
