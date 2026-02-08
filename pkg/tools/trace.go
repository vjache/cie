// Copyright 2025 KrakLabs
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// For commercial licensing, contact: licensing@kraklabs.com
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/kraklabs/cie/pkg/sigparse"
)

// TraceFuncInfo holds function metadata for call path tracing
type TraceFuncInfo struct {
	Name     string
	FilePath string
	Line     string
	CallLine string // Line in the caller where this function is called (empty = unknown)
}

// TracePathArgs holds arguments for tracing call paths
type TracePathArgs struct {
	Target      string
	Source      string
	PathPattern string
	MaxPaths    int
	MaxDepth    int
	Waypoints   []string // Intermediate functions the path must pass through, in order
}

// TracePath traces call paths from source function(s) to a target function.
// If waypoints are specified, chains BFS segments through each waypoint in order.
func TracePath(ctx context.Context, client Querier, args TracePathArgs) (*ToolResult, error) {
	if args.Target == "" {
		return NewError("Error: 'target' function name is required"), nil
	}

	// If waypoints are provided, use segmented tracing
	if len(args.Waypoints) > 0 {
		return traceWithWaypoints(ctx, client, args)
	}

	// Find source and target functions
	sources, err := getTraceSources(ctx, client, args)
	if err != nil {
		return NewResult(err.Error()), nil
	}
	targets := findFunctionsByName(ctx, client, args.Target, args.PathPattern)
	if len(targets) == 0 {
		return NewResult(notFoundWithSuggestions(ctx, client,
			fmt.Sprintf("Target function '%s' not found.", args.Target),
			args.Target, args.PathPattern)), nil
	}

	// Build target set for quick lookup
	targetSet := make(map[string]bool)
	for _, t := range targets {
		targetSet[t.Name] = true
	}

	// Run BFS search
	searchResult := runTraceSearch(ctx, client, sources, targetSet, args)
	if searchResult.canceled {
		return NewResult("Search canceled (timeout or cancellation)."), nil
	}

	// Format and return output
	if len(searchResult.paths) == 0 {
		// Detect interface boundary at the last function in the deepest path
		if len(searchResult.deepestPath) > 0 {
			lastFn := searchResult.deepestPath[len(searchResult.deepestPath)-1]
			searchResult.interfaceBoundary = detectInterfaceBoundary(ctx, client, lastFn.Name)
		}
		return NewResult(formatTraceNotFound(sources, args, searchResult)), nil
	}
	return NewResult(formatTraceOutput(sources, args, searchResult)), nil
}

// traceWithWaypoints chains BFS segments through waypoints: source → wp1 → wp2 → ... → target.
// Each segment uses the same args for MaxDepth and PathPattern.
func traceWithWaypoints(ctx context.Context, client Querier, args TracePathArgs) (*ToolResult, error) {
	// Build ordered list of stops: [source, wp1, wp2, ..., target]
	stops := make([]string, 0, len(args.Waypoints)+2)
	if args.Source != "" {
		stops = append(stops, args.Source)
	}
	stops = append(stops, args.Waypoints...)
	stops = append(stops, args.Target)

	var fullPath []TraceFuncInfo
	totalNodes := 0

	for i := 0; i < len(stops)-1; i++ {
		segSource := stops[i]
		segTarget := stops[i+1]

		segArgs := TracePathArgs{
			Target:      segTarget,
			Source:       segSource,
			PathPattern: args.PathPattern,
			MaxPaths:    1, // Only need one path per segment
			MaxDepth:    args.MaxDepth,
		}

		// Find source functions for this segment
		sources := findFunctionsByName(ctx, client, segSource, args.PathPattern)
		if len(sources) == 0 {
			if i == 0 && args.Source == "" {
				sources = detectEntryPoints(ctx, client, args.PathPattern)
			}
			if len(sources) == 0 {
				return NewResult(notFoundWithSuggestions(ctx, client,
					fmt.Sprintf("Waypoint segment failed: function '%s' not found (segment %d: %s → %s).",
						segSource, i+1, segSource, segTarget),
					segSource, args.PathPattern)), nil
			}
		}

		targets := findFunctionsByName(ctx, client, segTarget, args.PathPattern)
		if len(targets) == 0 {
			return NewResult(notFoundWithSuggestions(ctx, client,
				fmt.Sprintf("Waypoint segment failed: function '%s' not found (segment %d: %s → %s).",
					segTarget, i+1, segSource, segTarget),
				segTarget, args.PathPattern)), nil
		}

		targetSet := make(map[string]bool)
		for _, t := range targets {
			targetSet[t.Name] = true
		}

		segResult := runTraceSearch(ctx, client, sources, targetSet, segArgs)
		totalNodes += segResult.nodesExplored

		if segResult.canceled {
			return NewResult(fmt.Sprintf("Search canceled during segment %d (%s → %s).",
				i+1, segSource, segTarget)), nil
		}

		if len(segResult.paths) == 0 {
			return NewResult(fmt.Sprintf("No path found for segment %d: %s → %s (explored %d nodes).\n\n"+
				"The waypoint chain broke at this segment. Try:\n"+
				"- Verify both functions exist with `cie_find_function`\n"+
				"- Increase `max_depth` if the functions are far apart\n"+
				"- Check that a call path exists between these functions\n",
				i+1, segSource, segTarget, segResult.nodesExplored)), nil
		}

		// Concatenate segment path (skip first node for subsequent segments to avoid duplicates)
		segPath := segResult.paths[0]
		if i > 0 && len(segPath) > 0 {
			segPath = segPath[1:] // skip junction node (already in fullPath)
		}
		fullPath = append(fullPath, segPath...)
	}

	// Format output
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Call Path to `%s` (via %d waypoint(s))\n\n", args.Target, len(args.Waypoints))
	fmt.Fprintf(&sb, "_Explored %d total nodes across %d segment(s)._\n\n", totalNodes, len(stops)-1)
	sb.WriteString("```\n")
	for i, fn := range fullPath {
		indent := strings.Repeat("  ", i)
		arrow := ""
		if i > 0 {
			arrow = "→ "
		}
		fmt.Fprintf(&sb, "%s%s%s\n", indent, arrow, fn.Name)
		locInfo := fmt.Sprintf("%s:%s", ExtractFileName(fn.FilePath), fn.Line)
		if fn.CallLine != "" && i > 0 {
			prevFile := ExtractFileName(fullPath[i-1].FilePath)
			locInfo += fmt.Sprintf("  [called at %s:%s]", prevFile, fn.CallLine)
		}
		fmt.Fprintf(&sb, "%s   %s\n", indent, locInfo)
	}
	sb.WriteString("```\n")

	return NewResult(sb.String()), nil
}

// getTraceSources finds source functions for tracing.
func getTraceSources(ctx context.Context, client Querier, args TracePathArgs) ([]TraceFuncInfo, error) {
	if args.Source == "" {
		sources := detectEntryPoints(ctx, client, args.PathPattern)
		if len(sources) == 0 {
			return nil, fmt.Errorf("no entry points found: try specifying a 'source' function explicitly")
		}
		return sources, nil
	}
	sources := findFunctionsByName(ctx, client, args.Source, args.PathPattern)
	if len(sources) == 0 {
		return nil, fmt.Errorf("%s", notFoundWithSuggestions(ctx, client,
			fmt.Sprintf("source function %q not found", args.Source),
			args.Source, args.PathPattern))
	}
	return sources, nil
}

// interfaceBoundaryInfo describes where a trace stopped at an interface boundary.
type interfaceBoundaryInfo struct {
	FunctionName   string   // The function where the trace stopped
	InterfaceNames []string // Interface types found (fields or params)
}

// traceSearchResult holds the result of a trace search.
type traceSearchResult struct {
	paths              [][]TraceFuncInfo
	nodesExplored      int
	limitReached       bool
	canceled           bool
	deepestPath        []TraceFuncInfo      // longest partial path explored (when no full path found)
	interfaceBoundary  *interfaceBoundaryInfo // detected interface boundary (when no full path found)
}

// pathNode represents a node in the BFS traversal.
type pathNode struct {
	funcName string
	path     []TraceFuncInfo
}

// runTraceSearch performs BFS search from sources to targets.
func runTraceSearch(ctx context.Context, client Querier, sources []TraceFuncInfo, targetSet map[string]bool, args TracePathArgs) traceSearchResult {
	const maxNodesExplored = 5000
	const maxQueriesPerSource = 1000

	result := traceSearchResult{}
	calleesCache := make(map[string][]TraceFuncInfo)

	for _, src := range sources {
		if len(result.paths) >= args.MaxPaths {
			break
		}
		select {
		case <-ctx.Done():
			result.canceled = true
			return result
		default:
		}

		srcResult := searchFromSource(ctx, client, src, targetSet, args, calleesCache, &result.nodesExplored, maxNodesExplored, maxQueriesPerSource)
		result.paths = append(result.paths, srcResult.paths...)
		// Track the deepest partial path across all sources
		if len(srcResult.deepestPath) > len(result.deepestPath) {
			result.deepestPath = srcResult.deepestPath
		}
		if srcResult.limitReached {
			result.limitReached = true
			break
		}
		if srcResult.canceled {
			result.canceled = true
			return result
		}
	}
	return result
}

// searchFromSource performs BFS from a single source function.
func searchFromSource(ctx context.Context, client Querier, src TraceFuncInfo, targetSet map[string]bool, args TracePathArgs, calleesCache map[string][]TraceFuncInfo, totalNodes *int, maxNodes, maxQueries int) traceSearchResult {
	result := traceSearchResult{}
	visited := make(map[string]bool)
	queue := []pathNode{{funcName: src.Name, path: []TraceFuncInfo{src}}}
	queries := 0

	for len(queue) > 0 && len(result.paths) < args.MaxPaths {
		if *totalNodes >= maxNodes || queries >= maxQueries {
			result.limitReached = true
			return result
		}
		if *totalNodes%100 == 0 {
			select {
			case <-ctx.Done():
				result.canceled = true
				return result
			default:
			}
		}

		current := queue[0]
		queue = queue[1:]

		if len(current.path) > args.MaxDepth {
			continue
		}

		// Check target BEFORE marking visited — allows multiple paths to reach
		// the same target through different routes (e.g., via test impl at depth 2
		// AND via real impl at depth 3). Without this, the first path to reach
		// the target marks it visited, blocking all alternate paths.
		if targetSet[current.funcName] && len(current.path) > 1 {
			result.paths = append(result.paths, current.path)
			continue // Don't expand target's callees or mark it visited
		}

		if visited[current.funcName] {
			continue
		}
		visited[current.funcName] = true
		*totalNodes++

		// Track deepest partial path for diagnostic output
		if len(current.path) > len(result.deepestPath) {
			result.deepestPath = current.path
		}

		callees, cached := calleesCache[current.funcName]
		if !cached {
			callees = getCallees(ctx, client, current.funcName)
			calleesCache[current.funcName] = callees
			queries++
		}

		for _, callee := range callees {
			if !visited[callee.Name] {
				newPath := make([]TraceFuncInfo, len(current.path), len(current.path)+1)
				copy(newPath, current.path)
				queue = append(queue, pathNode{funcName: callee.Name, path: append(newPath, callee)})
			}
		}
	}
	return result
}

// formatTraceNotFound formats the output when no paths are found.
func formatTraceNotFound(sources []TraceFuncInfo, args TracePathArgs, result traceSearchResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "No path found from %s to '%s' within depth %d.\n\n",
		formatSources(sources, args.Source == ""), args.Target, args.MaxDepth)
	fmt.Fprintf(&sb, "_Explored %d nodes before stopping._\n\n", result.nodesExplored)

	// Show partial path to help diagnose where the trace got stuck
	if len(result.deepestPath) > 1 {
		sb.WriteString("**Deepest partial path explored:**\n```\n")
		for i, fn := range result.deepestPath {
			indent := strings.Repeat("  ", i)
			arrow := ""
			if i > 0 {
				arrow = "→ "
			}
			fmt.Fprintf(&sb, "%s%s%s\n", indent, arrow, fn.Name)
			fmt.Fprintf(&sb, "%s   %s:%s\n", indent, ExtractFileName(fn.FilePath), fn.Line)
		}
		lastFn := result.deepestPath[len(result.deepestPath)-1]
		if result.interfaceBoundary != nil {
			fmt.Fprintf(&sb, "```\n\n**Interface boundary detected at `%s`:**\n", lastFn.Name)
			for _, iface := range result.interfaceBoundary.InterfaceNames {
				fmt.Fprintf(&sb, "  - Calls through `%s` interface (not resolved as call edge)\n", iface)
			}
			sb.WriteString("\n**Suggested next steps:**\n")
			for _, iface := range result.interfaceBoundary.InterfaceNames {
				fmt.Fprintf(&sb, "- Run `cie_find_implementations(\"%s\")` to discover concrete types\n", iface)
			}
			sb.WriteString("- Re-index with `cie index --full` to generate interface dispatch edges\n\n")
		} else {
			fmt.Fprintf(&sb, "```\n_Chain stopped at `%s` — no outgoing calls reached the target._\n\n", lastFn.Name)
		}
	}

	if result.limitReached {
		sb.WriteString("**Note:** Search limit reached (explored 5000 nodes). The path may exist but wasn't found in the explored portion of the call graph.\n\n")
	}
	sb.WriteString("**Tips:**\n")
	sb.WriteString("- Try increasing `max_depth` if the target is deeply nested\n")
	sb.WriteString("- Use `path_pattern` to narrow the search scope (e.g., `path_pattern=\"apps/core\"`)\n")
	sb.WriteString("- Check if the target function name is correct with `cie_find_function`\n")
	sb.WriteString("- Specify a `source` function closer to the target to reduce search space\n")
	sb.WriteString("- Ensure the codebase was re-indexed with the latest CIE version (`cie index --full`)\n")
	return sb.String()
}

// formatTraceOutput formats the output when paths are found.
func formatTraceOutput(sources []TraceFuncInfo, args TracePathArgs, result traceSearchResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Call Paths to `%s`\n\n", args.Target)
	fmt.Fprintf(&sb, "Found %d path(s) from %s\n", len(result.paths), formatSources(sources, args.Source == ""))
	fmt.Fprintf(&sb, "_Explored %d nodes._\n\n", result.nodesExplored)

	for i, path := range result.paths {
		fmt.Fprintf(&sb, "### Path %d (depth: %d)\n\n```\n", i+1, len(path)-1)
		for j, fn := range path {
			indent := strings.Repeat("  ", j)
			arrow := ""
			if j > 0 {
				arrow = "→ "
			}
			fmt.Fprintf(&sb, "%s%s%s\n", indent, arrow, fn.Name)
			locInfo := fmt.Sprintf("%s:%s", ExtractFileName(fn.FilePath), fn.Line)
			if fn.CallLine != "" {
				// Show where in the previous function this call happens
				prevFile := ExtractFileName(path[j-1].FilePath)
				locInfo += fmt.Sprintf("  [called at %s:%s]", prevFile, fn.CallLine)
			}
			fmt.Fprintf(&sb, "%s   %s\n", indent, locInfo)
		}
		sb.WriteString("```\n\n")
	}

	if len(result.paths) >= args.MaxPaths {
		fmt.Fprintf(&sb, "*Showing first %d paths. Use `max_paths` to see more.*\n", args.MaxPaths)
	}
	if result.limitReached {
		sb.WriteString("\n**Note:** Search limit reached. There may be additional paths not shown.\n")
	}
	return sb.String()
}

// detectEntryPoints finds entry point functions based on language conventions
func detectEntryPoints(ctx context.Context, client Querier, pathPattern string) []TraceFuncInfo {
	var results []TraceFuncInfo

	// Entry point patterns for different languages
	// Go/Rust: main functions
	// JS/TS: exports in index/app/server files
	// Python: __main__ blocks (represented as functions)
	// Note: Use [.] instead of \. for CozoDB regex compatibility
	patterns := []struct {
		namePattern string
		filePattern string
	}{
		// Go: main function
		{`^main$`, `[.]go$`},
		// Rust: main function
		{`^main$`, `[.]rs$`},
		// JS/TS: common entry point file patterns
		{`.*`, `(index|app|server|main)[.](js|ts|mjs|cjs)$`},
		// Python: module entry points
		{`^(__main__|main)$`, `[.]py$`},
	}

	for _, p := range patterns {
		var conditions []string
		conditions = append(conditions, fmt.Sprintf("regex_matches(name, %q)", p.namePattern))
		conditions = append(conditions, fmt.Sprintf("regex_matches(file_path, %q)", p.filePattern))
		if pathPattern != "" {
			conditions = append(conditions, fmt.Sprintf("regex_matches(file_path, %q)", pathPattern))
		}
		// Exclude test files (use [.] instead of \. for CozoDB compatibility)
		conditions = append(conditions, `!regex_matches(file_path, "_test[.]go|test_|[.]test[.](js|ts)")`)

		script := fmt.Sprintf(
			"?[name, file_path, start_line] := *cie_function { name, file_path, start_line }, %s :limit 20",
			strings.Join(conditions, ", "),
		)

		result, err := client.Query(ctx, script)
		if err != nil {
			continue
		}

		for _, row := range result.Rows {
			results = append(results, TraceFuncInfo{
				Name:     AnyToString(row[0]),
				FilePath: AnyToString(row[1]),
				Line:     AnyToString(row[2]),
			})
		}
	}

	return results
}

// findFunctionsByName finds functions matching a name pattern
func findFunctionsByName(ctx context.Context, client Querier, name, pathPattern string) []TraceFuncInfo {
	var conditions []string
	// Case-insensitive match: exact name OR method suffix (e.g., "Run" matches "Agent.Run")
	namePattern := fmt.Sprintf("(?i)^%s$", EscapeRegex(name))
	methodPattern := fmt.Sprintf("(?i)[.]%s$", EscapeRegex(name))
	conditions = append(conditions, fmt.Sprintf("(regex_matches(name, %q) or regex_matches(name, %q))", namePattern, methodPattern))
	if pathPattern != "" {
		conditions = append(conditions, fmt.Sprintf("regex_matches(file_path, %q)", pathPattern))
	}

	script := fmt.Sprintf(
		"?[name, file_path, start_line] := *cie_function { name, file_path, start_line }, %s :limit 50",
		strings.Join(conditions, ", "),
	)

	result, err := client.Query(ctx, script)
	if err != nil {
		return nil
	}

	var ret []TraceFuncInfo
	for _, row := range result.Rows {
		ret = append(ret, TraceFuncInfo{
			Name:     AnyToString(row[0]),
			FilePath: AnyToString(row[1]),
			Line:     AnyToString(row[2]),
		})
	}
	return ret
}

// getCallees returns functions called by the given function.
// Includes both direct call edges (cie_calls) and interface dispatch
// (cie_field + cie_implements → concrete method implementations).
func getCallees(ctx context.Context, client Querier, funcName string) []TraceFuncInfo {
	// 1. Direct callees via cie_calls (includes call_line for callsite info)
	script := fmt.Sprintf(
		`?[callee_name, callee_file, callee_line, call_line] :=
			*cie_calls { caller_id, callee_id, call_line },
			*cie_function { id: caller_id, name: caller_name },
			*cie_function { id: callee_id, file_path: callee_file, name: callee_name, start_line: callee_line },
			(caller_name = %q or ends_with(caller_name, %q))
		:limit 100`,
		funcName, "."+funcName,
	)

	result, err := client.Query(ctx, script)
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var ret []TraceFuncInfo
	for _, row := range result.Rows {
		name := AnyToString(row[0])
		seen[name] = true
		callLine := ""
		if len(row) > 3 {
			cl := AnyToString(row[3])
			if cl != "" && cl != "0" {
				callLine = cl
			}
		}
		ret = append(ret, TraceFuncInfo{
			Name:     name,
			FilePath: AnyToString(row[1]),
			Line:     AnyToString(row[2]),
			CallLine: callLine,
		})
	}

	// Extract called method names from source code — shared by Phase 2/2b/3
	// to filter results to only methods actually invoked in the function body.
	calledMethods := extractCalledMethodsFromCode(ctx, client, funcName)

	// 2 & 2b. Field-based dispatch (interface + concrete)
	structName := extractStructName(funcName)
	if structName != "" {
		fieldCallees := getCalleesViaFields(ctx, client, funcName, structName, seen, calledMethods)
		ret = append(ret, fieldCallees...)
	}

	// 3. Parameter-based interface dispatch
	// Always run: a method can have BOTH field-based callees (Phase 2) AND
	// parameter-based interface calls.
	paramCallees := getCalleesViaParams(ctx, client, funcName, seen, calledMethods)
	ret = append(ret, paramCallees...)

	return ret
}

// getCalleesViaFields resolves callees through struct field types.
// Phase 2: interface-typed fields → concrete implementations.
// Phase 2b: concrete-typed fields → direct method lookup.
// Uses source code analysis to filter results to only actually called methods.
func getCalleesViaFields(ctx context.Context, client Querier, funcName, structName string, seen map[string]bool, calledMethods map[string]bool) []TraceFuncInfo {
	var ret []TraceFuncInfo

	// Phase 2: Interface field dispatch
	dispatchScript := fmt.Sprintf(
		`?[callee_name, callee_file, callee_line] :=
			*cie_field { struct_name: %q, field_type },
			*cie_implements { interface_name },
			(field_type = interface_name or ends_with(field_type, concat(".", interface_name))),
			*cie_implements { interface_name, type_name: impl_type },
			impl_prefix = concat(impl_type, "."),
			*cie_function { name: callee_name, file_path: callee_file, start_line: callee_line },
			starts_with(callee_name, impl_prefix),
			not regex_matches(callee_file, "_test[.]go$")
		:limit 50`,
		structName,
	)
	dispatchResult, err := client.Query(ctx, dispatchScript)
	if err == nil {
		ret = appendFilteredCallees(ret, dispatchResult, seen, calledMethods)
	}

	// Phase 2b: Concrete field dispatch
	concreteScript := fmt.Sprintf(
		`?[callee_name, callee_file, callee_line] :=
			*cie_field { struct_name: %q, field_type },
			field_prefix = concat(field_type, "."),
			*cie_function { name: callee_name, file_path: callee_file, start_line: callee_line },
			starts_with(callee_name, field_prefix)
		:limit 50`,
		structName,
	)
	concreteResult, err := client.Query(ctx, concreteScript)
	if err == nil {
		ret = appendFilteredCallees(ret, concreteResult, seen, calledMethods)
	}

	return ret
}

// appendFilteredCallees adds query results to ret, filtering by seen map and
// optionally by calledMethods (method names extracted from source code).
func appendFilteredCallees(ret []TraceFuncInfo, result *QueryResult, seen map[string]bool, calledMethods map[string]bool) []TraceFuncInfo {
	for _, row := range result.Rows {
		name := AnyToString(row[0])
		if seen[name] {
			continue
		}
		if calledMethods != nil {
			if !calledMethods[extractMethodName(name)] {
				continue
			}
		}
		seen[name] = true
		ret = append(ret, TraceFuncInfo{
			Name:     name,
			FilePath: AnyToString(row[1]),
			Line:     AnyToString(row[2]),
		})
	}
	return ret
}

// getCalleesViaParams resolves interface dispatch through function parameters.
// Queries the function's signature, parses parameter types, and for each interface-typed
// parameter, finds concrete implementations and their methods.
// calledMethods (from source code analysis) filters results to only methods actually called.
func getCalleesViaParams(ctx context.Context, client Querier, funcName string, seen map[string]bool, calledMethods map[string]bool) []TraceFuncInfo {
	// Query the function's signature
	sigScript := fmt.Sprintf(
		`?[signature] := *cie_function { name, signature }, (name = %q or ends_with(name, %q)) :limit 1`,
		funcName, "."+funcName,
	)
	sigResult, err := client.Query(ctx, sigScript)
	if err != nil || len(sigResult.Rows) == 0 {
		return nil
	}

	sig := AnyToString(sigResult.Rows[0][0])
	if sig == "" {
		return nil
	}

	params := sigparse.ParseGoParams(sig)
	if len(params) == 0 {
		return nil
	}

	// For each param with a non-primitive type, check if it's an interface
	var ret []TraceFuncInfo
	for _, p := range params {
		if isPrimitiveType(p.Type) {
			continue
		}

		// Query implementations of this type (exclude test files)
		implScript := fmt.Sprintf(
			`?[callee_name, callee_file, callee_line] :=
				*cie_implements { interface_name, type_name: impl_type },
				(interface_name = %q or ends_with(interface_name, %q)),
				impl_prefix = concat(impl_type, "."),
				*cie_function { name: callee_name, file_path: callee_file, start_line: callee_line },
				starts_with(callee_name, impl_prefix),
				not regex_matches(callee_file, "_test[.]go$")
			:limit 50`,
			p.Type, "."+p.Type,
		)

		implResult, err := client.Query(ctx, implScript)
		if err != nil || len(implResult.Rows) == 0 {
			continue
		}

		for _, row := range implResult.Rows {
			name := AnyToString(row[0])
			// Fan-out reduction: only include methods actually called in source code.
			if calledMethods != nil {
				methodName := extractMethodName(name)
				if methodName != "" && !calledMethods[methodName] {
					continue
				}
			}
			if !seen[name] {
				seen[name] = true
				ret = append(ret, TraceFuncInfo{
					Name:     name,
					FilePath: AnyToString(row[1]),
					Line:     AnyToString(row[2]),
				})
			}
		}
	}

	return ret
}

// selectorCallPattern matches method calls through selectors like `.Query(` or `.Execute(`
var selectorCallPattern = regexp.MustCompile(`\.([A-Z]\w*)\s*\(`)

// extractCalledMethodsFromCode parses function source code to find method names
// called through selectors (e.g., `.Query(`, `.Execute(`). Returns a set of
// method names. Used to filter Phase 2b results and reduce fan-out.
func extractCalledMethodsFromCode(ctx context.Context, client Querier, funcName string) map[string]bool {
	script := fmt.Sprintf(
		`?[code_text] := *cie_function_code { function_id, code_text }, *cie_function { id: function_id, name: %q } :limit 1`,
		funcName,
	)
	result, err := client.Query(ctx, script)
	if err != nil || len(result.Rows) == 0 {
		return nil
	}
	code := AnyToString(result.Rows[0][0])
	if code == "" {
		return nil
	}

	matches := selectorCallPattern.FindAllStringSubmatch(code, -1)
	if len(matches) == 0 {
		return nil
	}
	methods := make(map[string]bool, len(matches))
	for _, m := range matches {
		methods[m[1]] = true
	}
	return methods
}

// extractMethodName extracts the method name from a qualified function name.
// e.g., "CIEClient.StoreFact" → "StoreFact", "main" → ""
func extractMethodName(funcName string) string {
	if idx := strings.LastIndex(funcName, "."); idx >= 0 && idx < len(funcName)-1 {
		return funcName[idx+1:]
	}
	return ""
}

// isPrimitiveType returns true for Go built-in types that can never be interfaces.
func isPrimitiveType(t string) bool {
	switch t {
	case "string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "complex64", "complex128",
		"bool", "byte", "rune", "error", "func",
		"Context": // context.Context is common but not user-defined
		return true
	}
	return false
}

// detectInterfaceBoundary checks whether a function sits at an interface boundary.
// For methods: queries the struct's fields for interface types.
// For standalone functions: queries the signature for interface-typed params.
// Returns nil if no interface boundary is detected.
func detectInterfaceBoundary(ctx context.Context, client Querier, funcName string) *interfaceBoundaryInfo {
	var interfaceNames []string

	if structName := extractStructName(funcName); structName != "" {
		interfaceNames = append(interfaceNames, detectFieldInterfaces(ctx, client, structName)...)
	}

	interfaceNames = appendUniqueStrings(interfaceNames, detectParamInterfaces(ctx, client, funcName)...)

	if len(interfaceNames) == 0 {
		return nil
	}

	return &interfaceBoundaryInfo{
		FunctionName:   funcName,
		InterfaceNames: interfaceNames,
	}
}

// detectFieldInterfaces queries struct fields and returns those whose types are known interfaces.
func detectFieldInterfaces(ctx context.Context, client Querier, structName string) []string {
	fieldScript := fmt.Sprintf(
		`?[field_type] :=
			*cie_field { struct_name: %q, field_type },
			*cie_implements { interface_name },
			(field_type = interface_name or ends_with(field_type, concat(".", interface_name)))
		:limit 10`,
		structName,
	)
	fieldResult, err := client.Query(ctx, fieldScript)
	if err != nil {
		return nil
	}
	var names []string
	for _, row := range fieldResult.Rows {
		names = append(names, AnyToString(row[0]))
	}
	return names
}

// detectParamInterfaces queries a function's signature and returns parameter types that are known interfaces.
func detectParamInterfaces(ctx context.Context, client Querier, funcName string) []string {
	sigScript := fmt.Sprintf(
		`?[signature] := *cie_function { name, signature }, (name = %q or ends_with(name, %q)) :limit 1`,
		funcName, "."+funcName,
	)
	sigResult, err := client.Query(ctx, sigScript)
	if err != nil || len(sigResult.Rows) == 0 {
		return nil
	}
	sig := AnyToString(sigResult.Rows[0][0])
	if sig == "" {
		return nil
	}

	var names []string
	for _, p := range sigparse.ParseGoParams(sig) {
		if isPrimitiveType(p.Type) {
			continue
		}
		implScript := fmt.Sprintf(
			`?[interface_name] := *cie_implements { interface_name }, (interface_name = %q or ends_with(interface_name, %q)) :limit 1`,
			p.Type, "."+p.Type,
		)
		implResult, err := client.Query(ctx, implScript)
		if err == nil && len(implResult.Rows) > 0 {
			names = append(names, AnyToString(implResult.Rows[0][0]))
		}
	}
	return names
}

// appendUniqueStrings appends values from src to dst, skipping duplicates.
func appendUniqueStrings(dst []string, src ...string) []string {
	for _, s := range src {
		found := false
		for _, existing := range dst {
			if existing == s {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, s)
		}
	}
	return dst
}

// extractStructName extracts the struct name from a qualified method name.
// e.g., "Builder.Build" → "Builder", "main" → ""
func extractStructName(funcName string) string {
	if idx := strings.Index(funcName, "."); idx > 0 {
		return funcName[:idx]
	}
	return ""
}

// findFunctionSuggestions queries for functions with similar names when a lookup fails.
// Uses case-insensitive substring match to suggest alternatives.
func findFunctionSuggestions(ctx context.Context, client Querier, name, pathPattern string, limit int) []TraceFuncInfo {
	if limit <= 0 {
		limit = 5
	}

	var conditions []string
	// Substring match: name contains the search term (case-insensitive)
	conditions = append(conditions, fmt.Sprintf("regex_matches(name, \"(?i)%s\")", EscapeRegex(name)))
	if pathPattern != "" {
		conditions = append(conditions, fmt.Sprintf("regex_matches(file_path, %q)", pathPattern))
	}

	script := fmt.Sprintf(
		"?[name, file_path, start_line] := *cie_function { name, file_path, start_line }, %s :limit %d",
		strings.Join(conditions, ", "),
		limit,
	)

	result, err := client.Query(ctx, script)
	if err != nil || len(result.Rows) == 0 {
		return nil
	}

	var ret []TraceFuncInfo
	for _, row := range result.Rows {
		ret = append(ret, TraceFuncInfo{
			Name:     AnyToString(row[0]),
			FilePath: AnyToString(row[1]),
			Line:     AnyToString(row[2]),
		})
	}
	return ret
}

// notFoundWithSuggestions appends "Did you mean?" suggestions to a not-found message.
func notFoundWithSuggestions(ctx context.Context, client Querier, msg, name, pathPattern string) string {
	if suggestions := findFunctionSuggestions(ctx, client, name, pathPattern, 5); len(suggestions) > 0 {
		msg += formatSuggestions(suggestions)
	}
	return msg
}

// formatSuggestions formats function suggestions as a "Did you mean?" block.
func formatSuggestions(suggestions []TraceFuncInfo) string {
	if len(suggestions) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n**Did you mean?**\n")
	for _, fn := range suggestions {
		fmt.Fprintf(&sb, "- `%s` (%s:%s)\n", fn.Name, fn.FilePath, fn.Line)
	}
	return sb.String()
}

// formatSources formats the source list for display
func formatSources(sources []TraceFuncInfo, autoDetected bool) string {
	if len(sources) == 0 {
		return "unknown"
	}
	if len(sources) == 1 {
		return fmt.Sprintf("`%s`", sources[0].Name)
	}
	if autoDetected {
		return fmt.Sprintf("%d auto-detected entry points", len(sources))
	}
	return fmt.Sprintf("%d matching functions", len(sources))
}
