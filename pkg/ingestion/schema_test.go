package ingestion

import (
	"strings"
	"testing"
)

func TestDatalogSchema_ContainsFieldTable(t *testing.T) {
	schema := DatalogSchema()

	if !strings.Contains(schema, "cie_field") {
		t.Error("DatalogSchema() should contain cie_field table")
	}
	// Verify key columns exist
	for _, col := range []string{"struct_name", "field_name", "field_type"} {
		if !strings.Contains(schema, col) {
			t.Errorf("cie_field table should contain column %q", col)
		}
	}
}

func TestDatalogSchema_ContainsImplementsTable(t *testing.T) {
	schema := DatalogSchema()

	if !strings.Contains(schema, "cie_implements") {
		t.Error("DatalogSchema() should contain cie_implements table")
	}
	// Verify key columns exist
	for _, col := range []string{"type_name", "interface_name"} {
		if !strings.Contains(schema, col) {
			t.Errorf("cie_implements table should contain column %q", col)
		}
	}
}
