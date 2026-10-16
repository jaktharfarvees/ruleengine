package urnkey

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  Parts
	}{
		{name: "leave rule", value: "urn:arrowhr:rule:leave:eligibility:min-service:v2", want: Parts{Domain: "leave", RuleSet: "eligibility", Name: "min-service", Major: 2}},
		{name: "payroll rule", value: "urn:arrowhr:rule:payroll:overtime:payment:v1", want: Parts{Domain: "payroll", RuleSet: "overtime", Name: "payment", Major: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.value)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("Parse(%q) = %#v, want %#v", tt.value, got, tt.want)
			}
			if formatted := Format(got); formatted != tt.value {
				t.Fatalf("Format(Parse(%q)) = %q", tt.value, formatted)
			}
		})
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	tests := []string{
		"",
		"urn:other:rule:leave:eligibility:min-service:v1",
		"urn:arrowhr:rule:leave:eligibility:v1",
		"urn:arrowhr:rule::eligibility:min-service:v1",
		"urn:arrowhr:rule:leave::min-service:v1",
		"urn:arrowhr:rule:leave:eligibility::v1",
		"urn:arrowhr:rule:leave:eligibility:min-service:1",
		"urn:arrowhr:rule:leave:eligibility:min-service:v0",
		"urn:arrowhr:rule:leave:eligibility:min-service:vx",
		// A rule key is an identity, so only the canonical spelling of the
		// major version is accepted: no leading zero, no sign, no spaces.
		"urn:arrowhr:rule:leave:eligibility:min-service:v01",
		"urn:arrowhr:rule:leave:eligibility:min-service:v+1",
		"urn:arrowhr:rule:leave:eligibility:min-service:v 1",
		"urn:arrowhr:rule:leave:eligibility:min-service:v1_",
		"urn:arrowhr:rule:leave:eligibility:min-service:v99999999999999999999",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, err := Parse(value); err == nil {
				t.Fatalf("Parse(%q) error = nil, want rejection", value)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	f.Add("urn:arrowhr:rule:leave:eligibility:min-service:v1")
	f.Add("")
	f.Add("urn:arrowhr:rule:0:0:0:v01")
	f.Add("urn:arrowhr:rule:0:0:0:v+1")
	f.Add("urn:arrowhr:rule:a:b:c:v2147483648")
	f.Fuzz(func(t *testing.T, value string) {
		parts, err := Parse(value)
		if err == nil && Format(parts) != value {
			t.Fatalf("successful parse did not round-trip: %q", value)
		}
	})
}
