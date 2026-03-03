package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func assertEqual[T any](t *testing.T, label string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch\n got: %#v\nwant: %#v", label, got, want)
	}
}

func TestMainSource_DefaultScopesAreLimited_ExpectedOutputs(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go failed: %v", err)
	}

	requiredSnippets := []string{
		"scopes := []string{gmail.GmailReadonlyScope, gmail.GmailModifyScope, gmail.GmailSettingsBasicScope}",
	}

	missing := []string{}
	s := string(content)
	for _, snippet := range requiredSnippets {
		if !strings.Contains(s, snippet) {
			missing = append(missing, snippet)
		}
	}

	assertEqual(t, "required scope snippets missing", missing, []string{})
}

func TestExtractAuthCodeInput_ExpectedOutputs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "raw_code", in: "4/abc123", want: "4/abc123"},
		{name: "full_callback_url", in: "http://localhost/?state=state-token&code=4/abcXYZ&scope=a+b", want: "4/abcXYZ"},
		{name: "query_string_only", in: "state=state-token&code=4/queryOnly&scope=x", want: "4/queryOnly"},
		{name: "query_string_with_prefix", in: "?state=state-token&code=4/prefix&scope=x", want: "4/prefix"},
		{name: "trim_whitespace", in: "  4/trimmed  ", want: "4/trimmed"},
		{name: "empty_input", in: "   ", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAuthCodeInput(tc.in)
			assertEqual(t, "extractAuthCodeInput", got, tc.want)
		})
	}
}
