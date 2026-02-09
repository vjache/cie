// Copyright 2025 KrakLabs
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sigparse

import (
	"testing"
	"time"
)

func TestParseGoParams_Basic(t *testing.T) {
	tests := []struct {
		name      string
		signature string
		want      []ParamInfo
	}{
		{
			name:      "simple params",
			signature: "func foo(name string, age int) error",
			want: []ParamInfo{
				{Name: "name", Type: "string"},
				{Name: "age", Type: "int"},
			},
		},
		{
			name:      "grouped params",
			signature: "func foo(a, b int) error",
			want: []ParamInfo{
				{Name: "a", Type: "int"},
				{Name: "b", Type: "int"},
			},
		},
		{
			name:      "pointer type",
			signature: "func foo(s *Server) error",
			want: []ParamInfo{
				{Name: "s", Type: "Server"},
			},
		},
		{
			name:      "qualified type",
			signature: "func foo(ctx context.Context, q tools.Querier) error",
			want: []ParamInfo{
				{Name: "ctx", Type: "Context"},
				{Name: "q", Type: "Querier"},
			},
		},
		{
			name:      "method receiver excluded",
			signature: "func (s *Server) Run(ctx context.Context) error",
			want: []ParamInfo{
				{Name: "ctx", Type: "Context"},
			},
		},
		{
			name:      "func param",
			signature: "func foo(callback func(int) error) error",
			want: []ParamInfo{
				{Name: "callback", Type: "func"},
			},
		},
		{
			name:      "empty signature",
			signature: "",
			want:      nil,
		},
		{
			name:      "no params",
			signature: "func foo() error",
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseGoParams(tt.signature)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseGoParams(%q) returned %d params, want %d: %+v", tt.signature, len(got), len(tt.want), got)
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name || g.Type != tt.want[i].Type {
					t.Errorf("param[%d] = {%s, %s}, want {%s, %s}", i, g.Name, g.Type, tt.want[i].Name, tt.want[i].Type)
				}
			}
		})
	}
}

// TestParseGoParams_MapFunc reproduces the infinite loop bug where
// map[K]func() patterns caused splitParamTokens to hang forever.
// The inner loop used '(' and ')' as stop characters but never
// advanced past them, creating an infinite loop.
func TestParseGoParams_MapFunc(t *testing.T) {
	signatures := []string{
		"func Register(handlers map[string]func())",
		"func Register(handlers map[string]func(ctx context.Context) error)",
		"func foo(m map[string]func(int, int) bool, name string)",
		"func foo(ch chan func())",
		"func foo(x interface{ Method() })",
		"func foo(x interface{ Method(ctx context.Context) error })",
	}

	for _, sig := range signatures {
		t.Run(sig, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				ParseGoParams(sig)
				close(done)
			}()

			select {
			case <-done:
				// OK — completed without hanging
			case <-time.After(2 * time.Second):
				t.Fatalf("ParseGoParams(%q) hung — infinite loop detected", sig)
			}
		})
	}
}

func TestNormalizeType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Querier", "Querier"},
		{"*Querier", "Querier"},
		{"[]Querier", "Querier"},
		{"*[]Querier", "Querier"},
		{"tools.Querier", "Querier"},
		{"*tools.Querier", "Querier"},
		{"...string", "string"},
		{"func(int) error", "func"},
		{"interface{}", "interface{}"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeType(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractParamString(t *testing.T) {
	tests := []struct {
		name string
		sig  string
		want string
	}{
		{
			name: "simple function",
			sig:  "func foo(x int) error",
			want: "x int",
		},
		{
			name: "method with receiver",
			sig:  "func (s *Server) Run(ctx context.Context) error",
			want: "ctx context.Context",
		},
		{
			name: "no params",
			sig:  "func foo() error",
			want: "",
		},
		{
			name: "not a function",
			sig:  "var x int",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractParamString(tt.sig)
			if got != tt.want {
				t.Errorf("ExtractParamString(%q) = %q, want %q", tt.sig, got, tt.want)
			}
		})
	}
}