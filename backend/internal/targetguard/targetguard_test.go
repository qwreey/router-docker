package targetguard

import "testing"

// TestIsSelfHostCatchesEverySpelling is the regression test for the
// 2026-09-16 audit's F35: SelfHosts was matched as exact strings, so
// 127.0.0.2, 0.0.0.0 or an IPv4-mapped loopback slipped past it.
func TestIsSelfHostCatchesEverySpelling(t *testing.T) {
	for _, h := range []string{"localhost", "ROUTER", "forward", "127.0.0.1", "127.0.0.2", "127.255.255.254", "0.0.0.0", "::1", "[::1]", "::", "::ffff:127.0.0.1"} {
		if !IsSelfHost(h) {
			t.Errorf("IsSelfHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"code-docker", "dind", "10.0.0.1", "example.com"} {
		if IsSelfHost(h) {
			t.Errorf("IsSelfHost(%q) = true, want false", h)
		}
	}
}

// TestValidateHostRefusesDockerAPI covers the other half of F17: dind is an
// allowed host, but its Docker API port must never become a target - not
// even with a feature's allow-external opt-out set.
func TestValidateHostRefusesDockerAPI(t *testing.T) {
	allowed := map[string]bool{"code-docker": true, "dind": true}
	t.Setenv("TEST_ALLOW_EXTERNAL", "true")
	for _, port := range []string{"2375", "2376"} {
		if err := ValidateHost("dind", port, allowed, "TEST_ALLOW_EXTERNAL", "test"); err == nil {
			t.Errorf("ValidateHost(dind:%s) = nil, want refusal", port)
		}
		if err := Validate("dind:"+port, allowed, "TEST_ALLOW_EXTERNAL", "test"); err == nil {
			t.Errorf("Validate(dind:%s) = nil, want refusal", port)
		}
	}
	if err := ValidateHost("dind", "8080", allowed, "", "test"); err != nil {
		t.Errorf("ValidateHost(dind:8080) = %v, want ok", err)
	}
	if err := ValidateHost("elsewhere", "80", allowed, "", "test"); err == nil {
		t.Errorf("ValidateHost(elsewhere) with no opt-out = nil, want refusal")
	}
}
