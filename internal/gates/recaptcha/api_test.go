package recaptcha

import "testing"

func TestExtractRemoteIP(t *testing.T) {
	tests := map[string]string{
		"203.0.113.10:12345":  "203.0.113.10",
		"[2001:db8::1]:12345": "2001:db8::1",
		"2001:db8::2":         "2001:db8::2",
		"not an address":      "",
		"":                    "",
	}
	for input, want := range tests {
		if got := extractRemoteIP(input); got != want {
			t.Fatalf("extractRemoteIP(%q) = %q, want %q", input, got, want)
		}
	}
}
