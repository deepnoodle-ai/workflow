package domain

// EngineMode determines how the engine processes tasks.
type EngineMode string

const (
	// EngineModeEmbedded claims and executes tasks directly in-process.
	// Use this when the engine runs in the same process as task executors.
	EngineModeEmbedded EngineMode = "embedded"

	// EngineModeDistributed only creates tasks; workers claim them externally.
	// Use this for server deployments where separate worker processes execute tasks.
	EngineModeDistributed EngineMode = "distributed"
)
