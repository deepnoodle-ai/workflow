package risor

import risor "github.com/deepnoodle-ai/risor/v2"

// DefaultGlobals returns a baseline set of globals for Risor scripts used by
// the workflow engine. It includes the allowed builtins plus empty "inputs"
// and "state" placeholders so the compiler can resolve references at compile
// time. Additional extras are merged in after the builtins, allowing
// consumers to register custom builtins (e.g. a "print" function built with
// object.NewBuiltin).
func DefaultGlobals(extras ...map[string]any) map[string]any {
	allowed := allowedBuiltins()
	globals := make(map[string]any, len(allowed)+8)
	for name, value := range risor.Builtins() {
		if allowed[name] {
			globals[name] = value
		}
	}
	for _, extra := range extras {
		for name, value := range extra {
			globals[name] = value
		}
	}
	globals["inputs"] = map[string]any{}
	globals["state"] = map[string]any{}
	return globals
}

// allowedBuiltins returns the set of Risor built-in names that are permitted
// in workflow scripts. These are chosen to be deterministic and side-effect
// free so they can be run in any workflow context.
func allowedBuiltins() map[string]bool {
	return map[string]bool{
		"all":      true,
		"any":      true,
		"bool":     true,
		"byte":     true,
		"bytes":    true,
		"call":     true,
		"chunk":    true,
		"coalesce": true,
		"decode":   true,
		"encode":   true,
		"error":    true,
		"filter":   true,
		"float":    true,
		"getattr":  true,
		"int":      true,
		"keys":     true,
		"len":      true,
		"list":     true,
		"math":     true,
		"range":    true,
		"regexp":   true,
		"reversed": true,
		"sorted":   true,
		"sprintf":  true,
		"string":   true,
		"type":     true,
	}
}
