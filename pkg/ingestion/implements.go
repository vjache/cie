package ingestion

import (
	"regexp"
	"strings"
)

// interfaceMethodPattern matches method declarations in interface source code.
// Captures the method name from lines like "Write(data []byte) error" or "Flush() error".
var interfaceMethodPattern = regexp.MustCompile(`(?m)^\s*([A-Z][a-zA-Z0-9_]*)\s*\(`)

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
func extractInterfaceMethods(types []TypeEntity) []interfaceInfo {
	var result []interfaceInfo

	for _, t := range types {
		if t.Kind != "interface" {
			continue
		}
		methods := interfaceMethodPattern.FindAllStringSubmatch(t.CodeText, -1)
		var methodNames []string
		for _, m := range methods {
			if len(m) > 1 {
				methodNames = append(methodNames, m[1])
			}
		}
		result = append(result, interfaceInfo{
			name:    t.Name,
			methods: methodNames,
		})
	}

	return result
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
