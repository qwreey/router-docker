package netgate

import (
	"errors"
	"testing"
)

func TestAddForwardRejectsInvalidTargetHost(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	_, err := AddForward(path, Forward{HostPort: 8080, TargetHost: "10.0.0.1,evil", TargetPort: 80})
	if err == nil {
		t.Fatalf("AddForward(targetHost with comma) = nil error, want validation error")
	}
}

func TestAddForwardRejectsOutOfRangePort(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	_, err := AddForward(path, Forward{HostPort: 0, TargetHost: "10.0.0.1", TargetPort: 80})
	if err == nil {
		t.Fatalf("AddForward(hostPort=0) = nil error, want validation error")
	}
}

func TestAddForwardAcceptsValidInput(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	f, err := AddForward(path, Forward{HostPort: 8080, TargetHost: "code-docker", TargetPort: 80})
	if err != nil {
		t.Fatalf("AddForward(valid) = %v, want success", err)
	}
	if f.TargetHost != "code-docker" {
		t.Fatalf("AddForward() = %+v, want targetHost=code-docker", f)
	}
}

func TestSetBandwidthRejectsNegativeTotal(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	_, err := SetBandwidth(path, Bandwidth{TotalMbps: -1})
	if err == nil {
		t.Fatalf("SetBandwidth(totalMbps=-1) = nil error, want validation error")
	}
}

func TestSetBandwidthRejectsZeroServiceLimit(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	_, err := SetBandwidth(path, Bandwidth{Services: []ServiceLimit{{TargetHost: "code-docker", LimitMbps: 0}}})
	if err == nil {
		t.Fatalf("SetBandwidth(limitMbps=0) = nil error, want validation error")
	}
}

func TestSetBandwidthRejectsDuplicateTargetHost(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	_, err := SetBandwidth(path, Bandwidth{Services: []ServiceLimit{
		{TargetHost: "code-docker", LimitMbps: 10},
		{TargetHost: "code-docker", LimitMbps: 20},
	}})
	if err == nil {
		t.Fatalf("SetBandwidth(duplicate target_host) = nil error, want validation error")
	}
}

func TestSetBandwidthAcceptsValidInput(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	bw, err := SetBandwidth(path, Bandwidth{
		TotalMbps: 100,
		Services:  []ServiceLimit{{TargetHost: "code-docker", LimitMbps: 50}, {TargetHost: "dind", LimitMbps: 50}},
	})
	if err != nil {
		t.Fatalf("SetBandwidth(valid) = %v, want success", err)
	}
	if bw.TotalMbps != 100 || len(bw.Services) != 2 {
		t.Fatalf("SetBandwidth() = %+v, want totalMbps=100 with 2 services", bw)
	}
	got, err := GetBandwidth(path)
	if err != nil {
		t.Fatalf("GetBandwidth() = %v, want success", err)
	}
	if got.TotalMbps != 100 || len(got.Services) != 2 {
		t.Fatalf("GetBandwidth() = %+v, want totalMbps=100 with 2 services", got)
	}
}

func TestGetBandwidthOnMissingFileReturnsEmptyNotNil(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	got, err := GetBandwidth(path)
	if err != nil {
		t.Fatalf("GetBandwidth(missing file) = %v, want success", err)
	}
	if got.Services == nil {
		t.Fatalf("GetBandwidth(missing file).Services = nil, want empty slice (JSON null crashes the frontend)")
	}
}

func TestReplaceOutboundRejectsMalformedCIDR(t *testing.T) {
	// Each of these used to be written to the live config verbatim, where
	// firewall.default.sh's `iptables -A ... -d <cidr>` fails on the next
	// cycle and only logs - the UI kept showing a rule that was never in the
	// chain. See validateCIDR's doc comment.
	for _, cidr := range []string{
		"",
		"10.0.0.0/33",
		"10.0.0.0/",
		"not-a-cidr",
		"10.0.0.0 /8",
		"999.0.0.0/8",
		"10.0.0.0/8 -j ACCEPT",
	} {
		path := t.TempDir() + "/config.yaml"
		_, err := ReplaceOutbound(path, []OutboundRule{{Action: "block", CIDR: cidr}})
		if err == nil {
			t.Fatalf("ReplaceOutbound(cidr=%q) = nil error, want validation error", cidr)
		}
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("ReplaceOutbound(cidr=%q) = %v, want ErrValidation so the handler answers 400", cidr, err)
		}
	}
}

func TestReplaceOutboundAcceptsEveryValidForm(t *testing.T) {
	// v6 is accepted because firewall.default.sh mirrors ':'-containing
	// entries onto ip6tables; a bare address is accepted because
	// `iptables -d 8.8.8.8` is an ordinary single-host rule.
	rules := []OutboundRule{
		{Action: "block", CIDR: "10.0.0.0/8"},
		{Action: "allow", CIDR: "192.168.1.5"},
		{Action: "block", CIDR: "fc00::/7"},
		{Action: "allow", CIDR: "::1"},
	}
	path := t.TempDir() + "/config.yaml"
	got, err := ReplaceOutbound(path, rules)
	if err != nil {
		t.Fatalf("ReplaceOutbound(valid) = %v, want success", err)
	}
	if len(got) != len(rules) {
		t.Fatalf("ReplaceOutbound() returned %d rules, want %d", len(got), len(rules))
	}
}

func TestReplaceOutboundRejectsUnknownAction(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	_, err := ReplaceOutbound(path, []OutboundRule{{Action: "drop", CIDR: "10.0.0.0/8"}})
	if !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("ReplaceOutbound(action=drop) = %v, want ErrInvalidAction", err)
	}
}
