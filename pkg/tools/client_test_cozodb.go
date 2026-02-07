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
//go:build cozodb
// +build cozodb

// Test infrastructure for CozoDB integration tests.
// This file provides a test client that wraps CozoDB directly for testing.

package tools

import (
	"context"
	"fmt"
	"testing"

	cozo "github.com/kraklabs/cie/pkg/cozodb"
)

// TestCIEClient wraps a CozoDB instance for integration testing.
// It implements the same Query interface as CIEClient but executes locally.
type TestCIEClient struct {
	DB *cozo.CozoDB
}

// NewTestCIEClient creates a new test client wrapping a CozoDB instance.
func NewTestCIEClient(db *cozo.CozoDB) *TestCIEClient {
	return &TestCIEClient{DB: db}
}

// Query executes a CozoScript query directly against the embedded CozoDB.
func (c *TestCIEClient) Query(ctx context.Context, script string) (*QueryResult, error) {
	result, err := c.DB.Run(script, nil)
	if err != nil {
		return nil, fmt.Errorf("cozodb query: %w", err)
	}

	// Convert cozo.Result to QueryResult
	return &QueryResult{
		Headers: result.Headers,
		Rows:    result.Rows,
	}, nil
}

// QueryRaw executes a query and returns raw results.
func (c *TestCIEClient) QueryRaw(ctx context.Context, script string) (map[string]any, error) {
	result, err := c.DB.Run(script, nil)
	if err != nil {
		return nil, fmt.Errorf("cozodb raw query: %w", err)
	}

	return map[string]any{
		"Headers": result.Headers,
		"Rows":    result.Rows,
	}, nil
}

// openTestDB creates an in-memory CozoDB instance with the CIE schema for testing.
// The database is automatically cleaned up when the test ends.
func openTestDB(t testing.TB) *cozo.CozoDB {
	t.Helper()

	db, err := cozo.New("mem", "", nil)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	// Register cleanup
	t.Cleanup(func() {
		success := db.Close()
		if !success {
			t.Logf("Warning: Failed to close test database")
		}
	})

	// Create CIE schema
	if err := createCIESchema(&db); err != nil {
		t.Fatalf("Failed to create CIE schema: %v", err)
	}

	return &db
}

// createCIESchema creates all CIE tables in the database.
func createCIESchema(db *cozo.CozoDB) error {
	// Create cie_file table
	_, err := db.Run(`:create cie_file {
		id: String,
		path: String,
		hash: String,
		language: String,
		size: Int,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_file: %w", err)
	}

	// Create cie_function table
	_, err = db.Run(`:create cie_function {
		id: String,
		name: String,
		signature: String,
		file_path: String,
		start_line: Int,
		end_line: Int,
		start_col: Int,
		end_col: Int,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_function: %w", err)
	}

	// Create cie_function_code table
	_, err = db.Run(`:create cie_function_code {
		function_id: String,
		code_text: String,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_function_code: %w", err)
	}

	// Create cie_type table
	_, err = db.Run(`:create cie_type {
		id: String,
		name: String,
		kind: String,
		file_path: String,
		start_line: Int,
		end_line: Int,
		start_col: Int,
		end_col: Int,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_type: %w", err)
	}

	// Create cie_type_code table
	_, err = db.Run(`:create cie_type_code {
		type_id: String,
		code_text: String,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_type_code: %w", err)
	}

	// Create cie_defines edge table (file -> function)
	_, err = db.Run(`:create cie_defines {
		file_id: String,
		function_id: String,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_defines: %w", err)
	}

	// Create cie_defines_type edge table (file -> type)
	_, err = db.Run(`:create cie_defines_type {
		file_id: String,
		type_id: String,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_defines_type: %w", err)
	}

	// Create cie_calls edge table (function -> function)
	_, err = db.Run(`:create cie_calls {
		caller_id: String,
		callee_id: String,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_calls: %w", err)
	}

	// Create cie_field table (struct field → type mapping)
	_, err = db.Run(`:create cie_field {
		id: String =>
		struct_name: String,
		field_name: String,
		field_type: String,
		file_path: String,
		line: Int,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_field: %w", err)
	}

	// Create cie_implements table (concrete type → interface)
	_, err = db.Run(`:create cie_implements {
		id: String =>
		type_name: String,
		interface_name: String,
		file_path: String,
	}`, nil)
	if err != nil {
		return fmt.Errorf("create cie_implements: %w", err)
	}

	return nil
}

// insertTestFile inserts a test file into the database.
func insertTestFile(t testing.TB, db *cozo.CozoDB, id, path, language string) {
	t.Helper()

	script := `?[id, path, hash, language, size] <- [[$id, $path, $hash, $language, $size]]
		:put cie_file { id, path, hash, language, size }`

	params := map[string]any{
		"id":       id,
		"path":     path,
		"hash":     "test-hash-" + id,
		"language": language,
		"size":     1000,
	}

	_, err := db.Run(script, params)
	if err != nil {
		t.Fatalf("Failed to insert test file: %v", err)
	}
}

// insertTestFunction inserts a test function into the database.
func insertTestFunction(t testing.TB, db *cozo.CozoDB, id, name, filePath, signature, codeText string, startLine int) {
	t.Helper()

	// Insert function metadata
	script := `?[id, name, signature, file_path, start_line, end_line, start_col, end_col] <-
		[[$id, $name, $signature, $file_path, $start_line, $end_line, $start_col, $end_col]]
		:put cie_function { id, name, signature, file_path, start_line, end_line, start_col, end_col }`

	params := map[string]any{
		"id":         id,
		"name":       name,
		"signature":  signature,
		"file_path":  filePath,
		"start_line": startLine,
		"end_line":   startLine + 10,
		"start_col":  0,
		"end_col":    50,
	}

	_, err := db.Run(script, params)
	if err != nil {
		t.Fatalf("Failed to insert test function: %v", err)
	}

	// Insert function code
	codeScript := `?[function_id, code_text] <- [[$function_id, $code_text]]
		:put cie_function_code { function_id, code_text }`

	codeParams := map[string]any{
		"function_id": id,
		"code_text":   codeText,
	}

	_, err = db.Run(codeScript, codeParams)
	if err != nil {
		t.Fatalf("Failed to insert test function code: %v", err)
	}
}

// insertTestType inserts a test type into the database.
func insertTestType(t testing.TB, db *cozo.CozoDB, id, name, kind, filePath, codeText string, startLine int) {
	t.Helper()

	// Insert type metadata
	script := `?[id, name, kind, file_path, start_line, end_line, start_col, end_col] <-
		[[$id, $name, $kind, $file_path, $start_line, $end_line, $start_col, $end_col]]
		:put cie_type { id, name, kind, file_path, start_line, end_line, start_col, end_col }`

	params := map[string]any{
		"id":         id,
		"name":       name,
		"kind":       kind,
		"file_path":  filePath,
		"start_line": startLine,
		"end_line":   startLine + 10,
		"start_col":  0,
		"end_col":    50,
	}

	_, err := db.Run(script, params)
	if err != nil {
		t.Fatalf("Failed to insert test type: %v", err)
	}

	// Insert type code
	codeScript := `?[type_id, code_text] <- [[$type_id, $code_text]]
		:put cie_type_code { type_id, code_text }`

	codeParams := map[string]any{
		"type_id":   id,
		"code_text": codeText,
	}

	_, err = db.Run(codeScript, codeParams)
	if err != nil {
		t.Fatalf("Failed to insert test type code: %v", err)
	}
}

// insertTestField inserts a struct field entity.
func insertTestField(t testing.TB, db *cozo.CozoDB, id, structName, fieldName, fieldType, filePath string, line int) {
	t.Helper()

	script := `?[id, struct_name, field_name, field_type, file_path, line] <-
		[[$id, $struct_name, $field_name, $field_type, $file_path, $line]]
		:put cie_field { id => struct_name, field_name, field_type, file_path, line }`

	params := map[string]any{
		"id":          id,
		"struct_name": structName,
		"field_name":  fieldName,
		"field_type":  fieldType,
		"file_path":   filePath,
		"line":        line,
	}

	_, err := db.Run(script, params)
	if err != nil {
		t.Fatalf("Failed to insert test field: %v", err)
	}
}

// insertTestImplements inserts an implements relationship.
func insertTestImplements(t testing.TB, db *cozo.CozoDB, id, typeName, interfaceName, filePath string) {
	t.Helper()

	script := `?[id, type_name, interface_name, file_path] <-
		[[$id, $type_name, $interface_name, $file_path]]
		:put cie_implements { id => type_name, interface_name, file_path }`

	params := map[string]any{
		"id":             id,
		"type_name":      typeName,
		"interface_name": interfaceName,
		"file_path":      filePath,
	}

	_, err := db.Run(script, params)
	if err != nil {
		t.Fatalf("Failed to insert test implements: %v", err)
	}
}

// insertTestCall inserts a function call relationship.
func insertTestCall(t testing.TB, db *cozo.CozoDB, callID, callerID, calleeID string) {
	t.Helper()

	script := `?[caller_id, callee_id] <- [[$caller_id, $callee_id]]
		:put cie_calls { caller_id, callee_id }`

	params := map[string]any{
		"caller_id": callerID,
		"callee_id": calleeID,
	}

	_, err := db.Run(script, params)
	if err != nil {
		t.Fatalf("Failed to insert test call: %v", err)
	}
}
