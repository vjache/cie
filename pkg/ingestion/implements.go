package ingestion

import (
	"regexp"
	"strings"
)

// interfaceMethodPattern matches method declarations in interface source code.
// Captures the method name from lines like "Write(data []byte) error" or "Flush() error".
var interfaceMethodPattern = regexp.MustCompile(`(?m)^\s*([A-Z][a-zA-Z0-9_]*)\s*\(`)

// embeddedInterfacePattern matches embedded interface references inside interface bodies.
// Captures lines like "io.Reader", "Writer", or "fmt.Stringer" (bare type on its own line).
var embeddedInterfacePattern = regexp.MustCompile(`(?m)^\s+(\w+(?:\.\w+)?)\s*$`)

// stdlibInterfaceMethods maps common Go stdlib interface names to their methods.
// This allows resolving embedded stdlib interfaces (e.g., io.Reader) that aren't
// in the project's index. Only includes commonly-used interfaces.
var stdlibInterfaceMethods = map[string][]string{
	"Reader":    {"Read"},
	"Writer":    {"Write"},
	"Closer":    {"Close"},
	"Seeker":    {"Seek"},
	"Stringer":  {"String"},
	"Error":     {"Error"},
	"Handler":   {"ServeHTTP"},
	"Marshaler": {"MarshalJSON"},
}

// BuildImplementsIndex determines which concrete types implement which interfaces
// by matching method sets. A concrete type implements an interface if it has all
// methods declared by that interface.
func BuildImplementsIndex(types []TypeEntity, functions []FunctionEntity) []ImplementsEdge {
	// 1. Collect interfaces and their required methods
	interfaces := extractInterfaceMethods(types)

	// 2. Build method sets for concrete types from receiver methods
	typeMethods := buildTypeMethodSets(functions)

	// 3. Build a set of interface names for self-match prevention
	interfaceNames := make(map[string]bool)
	for _, iface := range interfaces {
		interfaceNames[iface.name] = true
	}

	// 4. Match: find concrete types that implement each interface
	var edges []ImplementsEdge
	for _, iface := range interfaces {
		if len(iface.methods) == 0 {
			continue
		}
		for typeName, methods := range typeMethods {
			// Skip self-match: interface doesn't implement itself
			if interfaceNames[typeName] {
				continue
			}
			if hasAllMethods(methods, iface.methods) {
				edges = append(edges, ImplementsEdge{
					TypeName:      typeName,
					InterfaceName: iface.name,
					FilePath:      typeFilePath(typeName, functions),
				})
			}
		}
	}

	return edges
}

type interfaceInfo struct {
	name    string
	methods []string
}

// extractInterfaceMethods extracts method names from interface type definitions.
// Uses a two-pass approach to resolve embedded interfaces:
//  1. First pass: extract explicit method declarations from all interfaces
//  2. Second pass: resolve embedded interface references and inherit their methods
func extractInterfaceMethods(types []TypeEntity) []interfaceInfo {
	// First pass: extract direct methods from all interfaces
	directMethods := make(map[string][]string) // name → method names
	for _, t := range types {
		if t.Kind != "interface" {
			continue
		}
		methods := interfaceMethodPattern.FindAllStringSubmatch(t.CodeText, -1)
		var names []string
		for _, m := range methods {
			if len(m) > 1 {
				names = append(names, m[1])
			}
		}
		directMethods[t.Name] = names
	}

	// Second pass: resolve embedded interfaces and inherit methods
	var result []interfaceInfo
	for _, t := range types {
		if t.Kind != "interface" {
			continue
		}
		allMethods := collectAllMethods(t, directMethods)
		var methodList []string
		for m := range allMethods {
			methodList = append(methodList, m)
		}
		result = append(result, interfaceInfo{
			name:    t.Name,
			methods: methodList,
		})
	}

	return result
}

// collectAllMethods gathers all methods for an interface, including those
// inherited from embedded interfaces (project-local or stdlib).
func collectAllMethods(t TypeEntity, directMethods map[string][]string) map[string]bool {
	allMethods := make(map[string]bool)
	for _, m := range directMethods[t.Name] {
		allMethods[m] = true
	}

	embeds := embeddedInterfacePattern.FindAllStringSubmatch(t.CodeText, -1)
	for _, embed := range embeds {
		baseName := stripPackagePrefix(embed[1])
		inheritMethods(allMethods, baseName, directMethods)
	}
	return allMethods
}

// stripPackagePrefix removes a package qualifier from a type reference.
// "io.Reader" → "Reader", "Writer" → "Writer".
func stripPackagePrefix(ref string) string {
	if idx := strings.LastIndex(ref, "."); idx >= 0 {
		return ref[idx+1:]
	}
	return ref
}

// inheritMethods adds methods from the named interface into the target set.
// Checks project-local interfaces first, then falls back to known stdlib interfaces.
func inheritMethods(target map[string]bool, name string, directMethods map[string][]string) {
	if methods, ok := directMethods[name]; ok {
		for _, m := range methods {
			target[m] = true
		}
		return
	}
	if methods, ok := stdlibInterfaceMethods[name]; ok {
		for _, m := range methods {
			target[m] = true
		}
	}
}

// buildTypeMethodSets builds a map of concrete type → set of method names
// from function entities with receiver syntax (e.g., "CozoDB.Write").
func buildTypeMethodSets(functions []FunctionEntity) map[string]map[string]bool {
	typeMethods := make(map[string]map[string]bool)

	for _, fn := range functions {
		if !strings.Contains(fn.Name, ".") {
			continue
		}
		parts := strings.SplitN(fn.Name, ".", 2)
		typeName := parts[0]
		methodName := parts[1]

		if typeMethods[typeName] == nil {
			typeMethods[typeName] = make(map[string]bool)
		}
		typeMethods[typeName][methodName] = true
	}

	return typeMethods
}

// hasAllMethods checks if the method set contains all required methods.
func hasAllMethods(methods map[string]bool, required []string) bool {
	for _, m := range required {
		if !methods[m] {
			return false
		}
	}
	return true
}

// typeFilePath finds the file path for a concrete type from its methods.
func typeFilePath(typeName string, functions []FunctionEntity) string {
	prefix := typeName + "."
	for _, fn := range functions {
		if strings.HasPrefix(fn.Name, prefix) {
			return fn.FilePath
		}
	}
	return ""
}
