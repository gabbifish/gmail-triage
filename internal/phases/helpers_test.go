package phases

import (
	"reflect"
	"testing"
)

func assertEqualLocal[T any](t *testing.T, label string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch\n got: %#v\nwant: %#v", label, got, want)
	}
}

func TestParseListUnsubscribe_ExpectedOutputs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "http_and_mailto_with_invalid_entries",
			in:   "<https://example.com/unsub>, <mailto:leave@example.com>, <not a url>, <https:///missing-host>",
			want: []string{"https://example.com/unsub", "mailto:leave@example.com"},
		},
		{
			name: "mailto_only",
			in:   "<mailto:unsubscribe@example.com?subject=unsubscribe>",
			want: []string{"mailto:unsubscribe@example.com?subject=unsubscribe"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseListUnsubscribe(tc.in)
			assertEqualLocal(t, "parsed unsubscribe endpoints", got, tc.want)
		})
	}
}

func TestDomainFromAddress_ExpectedOutputs(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{name: "subdomain_and_tld_removed", address: "user@emails.brooklinen.com", want: "brooklinen"},
		{name: "simple_domain", address: "person@gmail.com", want: "gmail"},
		{name: "country_tld", address: "person@updates.example.co.uk", want: "example"},
		{name: "invalid_address", address: "not-an-email", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domainFromAddress(tc.address)
			assertEqualLocal(t, "domainFromAddress", got, tc.want)
		})
	}
}
