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

// SearchTextArgs holds arguments for text search.
type SearchTextArgs struct {
	Pattern        string
	SearchIn       string // "code", "signature", "name", "all"
	FilePattern    string
	ExcludePattern string // Pattern to exclude (uses negate())
	Literal        bool   // If true, treat pattern as literal string (escape regex chars)
	Limit          int
}

// SearchText searches for text patterns in function code, signatures, or names.
// Schema v3: code_text is in separate cie_function_code table
func SearchText(ctx context.Context, client Querier, args SearchTextArgs) (*ToolResult, error) {
	if args.Pattern == "" {
		return NewError("Error: 'pattern' is required"), nil
	}

	if args.SearchIn == "" {
		args.SearchIn = "all"
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}

	// Validate regex if not in literal mode
	// Validate regex if not in literal mode
	if !args.Literal {
		if _, err := regexp.Compile(args.Pattern); err != nil {
			return NewError(fmt.Sprintf(
				"**Invalid Regex Pattern:**\n```\n%v\n```\n\n"+
					"The pattern `%s` is not valid regex: %v\n\n"+
					"### Solutions:\n"+
					"1. **Use literal mode** (recommended for exact matches):\n"+
					"   ```json\n"+
					"   {\"pattern\": \"%s\", \"literal\": true}\n"+
					"   ```\n\n"+
					"2. **Escape special characters manually:**\n"+
					"   Special regex chars: `.` `(` `)` `[` `]` `{` `}` `*` `+` `?` `^` `$` `|` `\\`\n"+
					"   Example: `.GET(` → `\\.GET\\(`\n\n"+
					"### Tip:\n"+
					"Use `literal: true` when searching for exact code patterns like `.GET(`, `->`, `::`, etc.",
				err, args.Pattern, err, args.Pattern)), nil
		}
	}

	// Escape pattern if literal mode is requested
	pattern := args.Pattern
	if args.Literal {
		pattern = EscapeRegex(pattern)
	}

	// Determine if we need to join with cie_function_code (only for code/all search)
	needsCodeJoin := args.SearchIn == "code" || args.SearchIn == "all"

	// Build query based on search target
	var conditions []string
	switch args.SearchIn {
	case "code":
		conditions = append(conditions, fmt.Sprintf("regex_matches(code_text, %q)", pattern))
	case "signature":
		conditions = append(conditions, fmt.Sprintf("regex_matches(signature, %q)", pattern))
	case "name":
		conditions = append(conditions, fmt.Sprintf("regex_matches(name, %q)", pattern))
	default: // "all"
		conditions = append(conditions, fmt.Sprintf("(regex_matches(name, %q) or regex_matches(signature, %q) or regex_matches(code_text, %q))", pattern, pattern, pattern))
	}

	if args.FilePattern != "" {
		conditions = append(conditions, fmt.Sprintf("regex_matches(file_path, %q)", args.FilePattern))
	}

	// Handle exclude pattern using negate() - CozoDB doesn't support negative lookahead
	if args.ExcludePattern != "" {
		conditions = append(conditions, fmt.Sprintf("negate(regex_matches(file_path, %q))", args.ExcludePattern))
	}

	// Schema v3: Join with cie_function_code only when searching in code
	var script string
	if needsCodeJoin {
		script = fmt.Sprintf(
			"?[file_path, name, signature, start_line, end_line] := *cie_function { id, file_path, name, signature, start_line, end_line }, *cie_function_code { function_id: id, code_text }, %s :limit %d",
			strings.Join(conditions, ", "),
			args.Limit,
		)
	} else {
		script = fmt.Sprintf(
			"?[file_path, name, signature, start_line, end_line] := *cie_function { file_path, name, signature, start_line, end_line }, %s :limit %d",
			strings.Join(conditions, ", "),
			args.Limit,
		)
	}

	result, err := client.Query(ctx, script)
	if err != nil {
		return NewError(fmt.Sprintf("Query error: %v\n\nGenerated query:\n%s", err, script)), nil
	}

	return NewResult(FormatQueryResult(result, script)), nil
}

// FindFunctionArgs holds arguments for finding functions.
type FindFunctionArgs struct {
	Name        string
	ExactMatch  bool
	IncludeCode bool
}

// FindFunction finds functions by name.
// Schema v3: code_text is in separate cie_function_code table
func FindFunction(ctx context.Context, client Querier, args FindFunctionArgs) (*ToolResult, error) {
	if args.Name == "" {
		return NewError("Error: 'name' is required"), nil
	}

	var condition string
	if args.ExactMatch {
		condition = fmt.Sprintf("name = %q", args.Name)
	} else {
		// Case-insensitive match: exact name OR methods ending with .Name
		namePattern := fmt.Sprintf("(?i)^%s$", EscapeRegex(args.Name))
		methodPattern := fmt.Sprintf("(?i)[.]%s$", EscapeRegex(args.Name))
		condition = fmt.Sprintf("(regex_matches(name, %q) or regex_matches(name, %q))", namePattern, methodPattern)
	}

	// Schema v3: Join with cie_function_code only when include_code is true
	var script string
	if args.IncludeCode {
		script = fmt.Sprintf("?[file_path, name, signature, start_line, end_line, code_text] := *cie_function { id, file_path, name, signature, start_line, end_line }, *cie_function_code { function_id: id, code_text }, %s", condition)
	} else {
		script = fmt.Sprintf("?[file_path, name, signature, start_line, end_line] := *cie_function { file_path, name, signature, start_line, end_line }, %s", condition)
	}

	result, err := client.Query(ctx, script)
	if err != nil {
		return NewError(fmt.Sprintf("Query error: %v\n\nGenerated query:\n%s", err, script)), nil
	}

	if len(result.Rows) == 0 {
		// Check if the name matches a type (struct, interface, etc.)
		typeScript := fmt.Sprintf(
			`?[name, kind] := *cie_type { name, kind }, regex_matches(name, "(?i)%s") :limit 3`,
			EscapeRegex(args.Name),
		)
		typeResult, typeErr := client.Query(ctx, typeScript)
		if typeErr == nil && len(typeResult.Rows) > 0 {
			var sb strings.Builder
			sb.WriteString(FormatQueryResult(result, script))
			sb.WriteString("\n\n**Did you mean a type?** Try `cie_find_type` instead:\n")
			for _, row := range typeResult.Rows {
				fmt.Fprintf(&sb, "- `%s` (%s)\n", AnyToString(row[0]), AnyToString(row[1]))
			}
			return NewResult(sb.String()), nil
		}
	}

	return NewResult(FormatQueryResult(result, script)), nil
}

// FindCallersArgs holds arguments for finding callers.
type FindCallersArgs struct {
	FunctionName    string
	IncludeIndirect bool
}

// FindCallers finds all functions that call a specific function.
// Includes both direct callers and callers through interface dispatch.
func FindCallers(ctx context.Context, client Querier, args FindCallersArgs) (*ToolResult, error) {
	if args.FunctionName == "" {
		return NewError("Error: 'function_name' is required"), nil
	}

	condition := fmt.Sprintf("(callee_name = %q or ends_with(callee_name, %q))", args.FunctionName, "."+args.FunctionName)

	script := fmt.Sprintf(`?[caller_file, caller_name, caller_line, callee_name] :=
  *cie_calls { caller_id, callee_id },
  *cie_function { id: callee_id, name: callee_name },
  *cie_function { id: caller_id, file_path: caller_file, name: caller_name, start_line: caller_line },
  %s`, condition)

	result, err := client.Query(ctx, script)
	if err != nil {
		return NewError(fmt.Sprintf("Query error: %v\n\nGenerated query:\n%s", err, script)), nil
	}

	// Also find callers through interface dispatch:
	// If FunctionName is "CozoDB.Write" and CozoDB implements Writer,
	// find structs with Writer-typed fields whose methods are callers.
	structName := extractStructName(args.FunctionName)
	if structName != "" {
		// Find callers through interface dispatch:
		// Look for structs that have a field typed as an interface that structName implements,
		// and whose methods are potential callers.
		dispatchScript := fmt.Sprintf(
			`?[caller_file, caller_name, caller_line, callee_name] :=
				callee_name = %q,
				*cie_implements { type_name: %q, interface_name },
				*cie_field { struct_name: caller_struct, field_type },
				(field_type = interface_name or ends_with(field_type, concat(".", interface_name))),
				caller_prefix = concat(caller_struct, "."),
				*cie_function { name: caller_name, file_path: caller_file, start_line: caller_line },
				starts_with(caller_name, caller_prefix)
			:limit 50`,
			args.FunctionName, structName)

		dispatchResult, dispatchErr := client.Query(ctx, dispatchScript)
		if dispatchErr == nil && len(dispatchResult.Rows) > 0 {
			result = mergeQueryResults(result, dispatchResult)
		}
	}

	return NewResult(FormatQueryResult(result, script)), nil
}

// FindCalleesArgs holds arguments for finding callees.
type FindCalleesArgs struct {
	FunctionName string
}

// FindCallees finds all functions called by a specific function.
// Includes both direct call edges and interface dispatch results.
func FindCallees(ctx context.Context, client Querier, args FindCalleesArgs) (*ToolResult, error) {
	if args.FunctionName == "" {
		return NewError("Error: 'function_name' is required"), nil
	}

	condition := fmt.Sprintf("(caller_name = %q or ends_with(caller_name, %q))", args.FunctionName, "."+args.FunctionName)

	script := fmt.Sprintf(`?[caller_name, callee_file, callee_name, callee_line] :=
  *cie_calls { caller_id, callee_id },
  *cie_function { id: caller_id, name: caller_name },
  *cie_function { id: callee_id, file_path: callee_file, name: callee_name, start_line: callee_line },
  %s`, condition)

	result, err := client.Query(ctx, script)
	if err != nil {
		return NewError(fmt.Sprintf("Query error: %v\n\nGenerated query:\n%s", err, script)), nil
	}

	// Also query interface dispatch callees.
	// Extract called method names from source code to filter and reduce fan-out.
	structName := extractStructName(args.FunctionName)
	if structName != "" {
		calledMethods := extractCalledMethodsFromCode(ctx, client, args.FunctionName)

		dispatchScript := fmt.Sprintf(
			`?[caller_name, callee_file, callee_name, callee_line] :=
				caller_name = %q,
				*cie_field { struct_name: %q, field_type },
				*cie_implements { interface_name },
				(field_type = interface_name or ends_with(field_type, concat(".", interface_name))),
				*cie_implements { interface_name, type_name: impl_type },
				impl_prefix = concat(impl_type, "."),
				*cie_function { name: callee_name, file_path: callee_file, start_line: callee_line },
				starts_with(callee_name, impl_prefix),
				not regex_matches(callee_file, "_test[.]go$")
			:limit 50`,
			args.FunctionName, structName,
		)

		dispatchResult, dispatchErr := client.Query(ctx, dispatchScript)
		if dispatchErr == nil && len(dispatchResult.Rows) > 0 {
			if calledMethods != nil {
				dispatchResult = filterResultsByCalledMethods(dispatchResult, calledMethods)
			}
			if len(dispatchResult.Rows) > 0 {
				result = mergeQueryResults(result, dispatchResult)
			}
		}

		// Concrete field dispatch (non-interface fields like *CozoDB)
		concreteScript := fmt.Sprintf(
			`?[caller_name, callee_file, callee_name, callee_line] :=
				caller_name = %q,
				*cie_field { struct_name: %q, field_type },
				field_prefix = concat(field_type, "."),
				*cie_function { name: callee_name, file_path: callee_file, start_line: callee_line },
				starts_with(callee_name, field_prefix)
			:limit 50`,
			args.FunctionName, structName,
		)
		concreteResult, concreteErr := client.Query(ctx, concreteScript)
		if concreteErr == nil && len(concreteResult.Rows) > 0 {
			if calledMethods != nil {
				concreteResult = filterResultsByCalledMethods(concreteResult, calledMethods)
			}
			if len(concreteResult.Rows) > 0 {
				result = mergeQueryResults(result, concreteResult)
			}
		}
	}

	// Parameter-based interface dispatch — always run, methods can have both
	// field-based callees and parameter-based interface calls.
	paramCallees := findCalleesViaParams(ctx, client, args.FunctionName)
	if paramCallees != nil && len(paramCallees.Rows) > 0 {
		result = mergeQueryResults(result, paramCallees)
	}

	return NewResult(FormatQueryResult(result, script)), nil
}

// findCalleesViaParams resolves interface dispatch through function parameter types.
// Queries the function's signature, parses params, and finds implementations.
func findCalleesViaParams(ctx context.Context, client Querier, funcName string) *QueryResult {
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

	var combined *QueryResult
	for _, p := range params {
		if isPrimitiveType(p.Type) {
			continue
		}

		implScript := fmt.Sprintf(
			`?[caller_name, callee_file, callee_name, callee_line] :=
				caller_name = %q,
				*cie_implements { interface_name, type_name: impl_type },
				(interface_name = %q or ends_with(interface_name, %q)),
				impl_prefix = concat(impl_type, "."),
				*cie_function { name: callee_name, file_path: callee_file, start_line: callee_line },
				starts_with(callee_name, impl_prefix),
				not regex_matches(callee_file, "_test[.]go$")
			:limit 50`,
			funcName, p.Type, "."+p.Type,
		)

		implResult, err := client.Query(ctx, implScript)
		if err != nil || len(implResult.Rows) == 0 {
			continue
		}

		if combined == nil {
			combined = implResult
		} else {
			combined = mergeQueryResults(combined, implResult)
		}
	}

	return combined
}

// ListFilesArgs holds arguments for listing files.
type ListFilesArgs struct {
	PathPattern string
	Language    string
	Limit       int
}

// ListFiles lists files in the indexed codebase.
func ListFiles(ctx context.Context, client Querier, args ListFilesArgs) (*ToolResult, error) {
	if args.Limit <= 0 {
		args.Limit = 50
	}

	var conditions []string
	if args.PathPattern != "" {
		conditions = append(conditions, fmt.Sprintf("regex_matches(path, %q)", args.PathPattern))
	}
	if args.Language != "" {
		conditions = append(conditions, fmt.Sprintf("language = %q", args.Language))
	}

	script := "?[path, language, size] := *cie_file { path, language, size }"
	if len(conditions) > 0 {
		script += ", " + strings.Join(conditions, ", ")
	}
	script += fmt.Sprintf(" :limit %d", args.Limit)

	result, err := client.Query(ctx, script)
	if err != nil {
		return NewError(fmt.Sprintf("Query error: %v\n\nGenerated query:\n%s", err, script)), nil
	}

	return NewResult(FormatQueryResult(result, script)), nil
}

// mergeQueryResults appends rows from src into dst, deduplicating by composite key of all columns.
// filterResultsByCalledMethods filters a QueryResult to only include rows where
// the callee_name column (index 2) has a method name matching the calledMethods set.
// Used to reduce Phase 2b fan-out from returning ALL methods of field types.
func filterResultsByCalledMethods(result *QueryResult, calledMethods map[string]bool) *QueryResult {
	var filtered [][]any
	for _, row := range result.Rows {
		if len(row) > 2 {
			calleeName := AnyToString(row[2])
			methodName := extractMethodName(calleeName)
			if calledMethods[methodName] {
				filtered = append(filtered, row)
			}
		}
	}
	return &QueryResult{Headers: result.Headers, Rows: filtered}
}

func mergeQueryResults(dst, src *QueryResult) *QueryResult {
	seen := make(map[string]bool)
	for _, row := range dst.Rows {
		seen[rowKey(row)] = true
	}
	for _, row := range src.Rows {
		key := rowKey(row)
		if !seen[key] {
			seen[key] = true
			dst.Rows = append(dst.Rows, row)
		}
	}
	return dst
}

// rowKey builds a composite dedup key from all columns of a row.
func rowKey(row []any) string {
	var sb strings.Builder
	for i, v := range row {
		if i > 0 {
			sb.WriteByte('|')
		}
		sb.WriteString(AnyToString(v))
	}
	return sb.String()
}

// FindBySignatureArgs holds arguments for finding functions by parameter or return type.
type FindBySignatureArgs struct {
	ParamType      string // Filter: functions with this param type (e.g., "Backend", "Querier")
	ReturnType     string // Filter: functions returning this type (e.g., "error", "Client")
	PathPattern    string // Scope to path
	ExcludePattern string // Exclude files matching pattern
	Limit          int
}

// sigMatchInfo holds a matched function for FindBySignature results.
type sigMatchInfo struct {
	Name      string
	FilePath  string
	Signature string
	Line      string
	ParamName string // matched param name (for param_type matches)
	ParamType string // matched param type
}

// FindBySignature searches functions by parameter type and/or return type.
// Uses regex on the signature field for a coarse filter, then post-filters with
// sigparse.ParseGoParams for precise parameter type matching.
func FindBySignature(ctx context.Context, client Querier, args FindBySignatureArgs) (*ToolResult, error) {
	if args.ParamType == "" && args.ReturnType == "" {
		return NewError("Error: at least one of 'param_type' or 'return_type' is required"), nil
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}

	script := buildSignatureQuery(args)
	result, err := client.Query(ctx, script)
	if err != nil {
		return NewError(fmt.Sprintf("Query error: %v\n\nGenerated query:\n%s", err, script)), nil
	}

	matches := filterSignatureMatches(result.Rows, args)
	return NewResult(formatSignatureMatches(matches, args)), nil
}

// buildSignatureQuery constructs the CozoScript query for signature-based search.
func buildSignatureQuery(args FindBySignatureArgs) string {
	var conditions []string
	if args.ParamType != "" {
		conditions = append(conditions, fmt.Sprintf("regex_matches(signature, \"(?i)%s\")", EscapeRegex(args.ParamType)))
	}
	if args.ReturnType != "" {
		conditions = append(conditions, fmt.Sprintf("regex_matches(signature, \"(?i)%s\")", EscapeRegex(args.ReturnType)))
	}
	if args.PathPattern != "" {
		conditions = append(conditions, fmt.Sprintf("regex_matches(file_path, %q)", args.PathPattern))
	}
	if args.ExcludePattern != "" {
		conditions = append(conditions, fmt.Sprintf("negate(regex_matches(file_path, %q))", args.ExcludePattern))
	}

	fetchLimit := args.Limit * 5
	if fetchLimit < 100 {
		fetchLimit = 100
	}

	return fmt.Sprintf(
		"?[name, file_path, signature, start_line] := *cie_function { name, file_path, signature, start_line }, %s :limit %d",
		strings.Join(conditions, ", "),
		fetchLimit,
	)
}

// filterSignatureMatches post-filters query results for precise type matching.
func filterSignatureMatches(rows [][]any, args FindBySignatureArgs) []sigMatchInfo {
	var matches []sigMatchInfo
	for _, row := range rows {
		if m, ok := matchSignatureRow(row, args); ok {
			matches = append(matches, m)
			if len(matches) >= args.Limit {
				break
			}
		}
	}
	return matches
}

// matchSignatureRow checks if a single row matches the signature criteria.
func matchSignatureRow(row []any, args FindBySignatureArgs) (sigMatchInfo, bool) {
	sig := AnyToString(row[2])
	m := sigMatchInfo{
		Name:      AnyToString(row[0]),
		FilePath:  AnyToString(row[1]),
		Signature: sig,
		Line:      AnyToString(row[3]),
	}

	if args.ParamType != "" {
		params := sigparse.ParseGoParams(sig)
		found := false
		for _, p := range params {
			if strings.EqualFold(p.Type, args.ParamType) {
				m.ParamName = p.Name
				m.ParamType = p.Type
				found = true
				break
			}
		}
		if !found {
			return m, false
		}
	}

	if args.ReturnType != "" {
		returnPart := extractReturnPart(sig)
		if returnPart == "" || !containsCaseInsensitive(returnPart, args.ReturnType) {
			return m, false
		}
	}

	return m, true
}

// formatSignatureMatches formats the results of a signature-based search.
func formatSignatureMatches(matches []sigMatchInfo, args FindBySignatureArgs) string {
	var sb strings.Builder
	switch {
	case args.ParamType != "" && args.ReturnType != "":
		fmt.Fprintf(&sb, "## Functions with parameter type `%s` and return type `%s`\n\n", args.ParamType, args.ReturnType)
	case args.ParamType != "":
		fmt.Fprintf(&sb, "## Functions with parameter type `%s`\n\n", args.ParamType)
	default:
		fmt.Fprintf(&sb, "## Functions with return type `%s`\n\n", args.ReturnType)
	}

	if len(matches) == 0 {
		sb.WriteString("No matching functions found.\n")
		return sb.String()
	}

	fmt.Fprintf(&sb, "Found %d function(s):\n\n", len(matches))
	for _, m := range matches {
		fmt.Fprintf(&sb, "**%s** (%s:%s)\n", m.Name, m.FilePath, m.Line)
		if len(m.Signature) < 120 {
			fmt.Fprintf(&sb, "  Signature: `%s`\n", m.Signature)
		}
		if m.ParamName != "" {
			fmt.Fprintf(&sb, "  Parameter: `%s` (%s)\n", m.ParamName, m.ParamType)
		}
		sb.WriteString("\n")
	}

	if len(matches) >= args.Limit {
		fmt.Fprintf(&sb, "_Showing first %d results. Increase `limit` for more._\n", args.Limit)
	}

	return sb.String()
}

// extractReturnPart extracts the return type portion from a Go function signature.
// Given "func (s *Server) Run(ctx Context) error", returns "error".
// Given "func Foo() (int, error)", returns "(int, error)".
func extractReturnPart(sig string) string {
	// Find the parameter list closing paren, then everything after is returns
	idx := strings.Index(sig, "func")
	if idx == -1 {
		return ""
	}

	// Skip past receiver if present
	pos := idx + 4
	pos = skipSpaces(sig, pos)
	if pos < len(sig) && sig[pos] == '(' {
		depth := 1
		pos++
		for pos < len(sig) && depth > 0 {
			switch sig[pos] {
			case '(':
				depth++
			case ')':
				depth--
			}
			pos++
		}
	}

	// Skip function name
	pos = skipSpaces(sig, pos)
	for pos < len(sig) && sig[pos] != '(' {
		pos++
	}
	if pos >= len(sig) {
		return ""
	}

	// Skip parameter list
	depth := 1
	pos++
	for pos < len(sig) && depth > 0 {
		switch sig[pos] {
		case '(':
			depth++
		case ')':
			depth--
		}
		pos++
	}

	// Everything after is the return part
	ret := strings.TrimSpace(sig[pos:])
	return ret
}

// skipSpaces advances past whitespace.
func skipSpaces(s string, pos int) int {
	for pos < len(s) && (s[pos] == ' ' || s[pos] == '\t') {
		pos++
	}
	return pos
}

// containsCaseInsensitive checks if s contains substr (case-insensitive).
func containsCaseInsensitive(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// RawQueryArgs holds arguments for raw queries.
type RawQueryArgs struct {
	Script string
}

// RawQuery executes a raw CozoScript query.
func RawQuery(ctx context.Context, client Querier, args RawQueryArgs) (*ToolResult, error) {
	if args.Script == "" {
		return NewError("Error: 'script' is required"), nil
	}

	result, err := client.Query(ctx, args.Script)
	if err != nil {
		return NewError(fmt.Sprintf("Query error: %v\n\nQuery:\n%s", err, args.Script)), nil
	}

	return NewResult(FormatQueryResult(result, args.Script)), nil
}
