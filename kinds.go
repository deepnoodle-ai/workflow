package workflow

// Kinds tells the library which step kinds the host executes and how to check
// their configuration. The library owns only the control kinds (join, set,
// sleep, wait); every other kind is the host's.
type Kinds interface {
	Known(kind string) bool
	// ValidateConfig checks a step's configuration: the kind key itself
	// plus every key the library does not reserve.
	ValidateConfig(kind string, config map[string]any) error
}

// KindMap is a [Kinds] built from a map. A nil validator accepts any config.
//
//	kinds := workflow.KindMap{
//		"agent":  validateAgent,
//		"action": nil,
//	}
type KindMap map[string]func(config map[string]any) error

func (k KindMap) Known(kind string) bool {
	_, ok := k[kind]
	return ok
}

func (k KindMap) ValidateConfig(kind string, config map[string]any) error {
	if fn := k[kind]; fn != nil {
		return fn(config)
	}
	return nil
}
