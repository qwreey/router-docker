package devproxy

import "testing"

func TestValidateHost(t *testing.T) {
	for _, ok := range []string{"app.example.com", "*.dev.example.com", "localhost", "a-b.c"} {
		if err := ValidateHost(ok); err != nil {
			t.Errorf("ValidateHost(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "*", "*.*", "a.*.com", "*example.com", "app.example.com.*", "a..b", "a b"} {
		if err := ValidateHost(bad); err == nil {
			t.Errorf("ValidateHost(%q) = nil, want refusal", bad)
		}
	}
}
