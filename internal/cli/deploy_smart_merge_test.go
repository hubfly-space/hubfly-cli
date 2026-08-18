package cli

import "testing"

func TestNormalizePortProtocolPreservesHTTP(t *testing.T) {
	if got := normalizePortProtocol("http"); got != "HTTP" {
		t.Fatalf("expected HTTP, got %q", got)
	}
}

func TestApplySmartCloudDefaultsMigratesLegacyHTTPPort(t *testing.T) {
	var cfg deployConfigFile
	cfg.Version = 1
	cfg.Deploy.Ports = []deployPort{{Container: 8080, Protocol: "TCP"}}
	var current deployContainerSnapshotResponse
	current.Container.Ports = []cliDeploymentPort{{ID: "port_http", Container: 8080, Protocol: "http"}}

	applySmartCloudDefaults(&cfg, &current)

	if got := cfg.Deploy.Ports[0]; got.ID != "port_http" || got.Protocol != "HTTP" {
		t.Fatalf("expected legacy port to adopt cloud HTTP identity, got %#v", got)
	}
}

func TestApplySmartCloudDefaultsDoesNotChangeV2Protocol(t *testing.T) {
	var cfg deployConfigFile
	cfg.Version = 2
	cfg.Deploy.Ports = []deployPort{{Container: 8080, Protocol: "TCP"}}
	var current deployContainerSnapshotResponse
	current.Container.Ports = []cliDeploymentPort{{ID: "port_http", Container: 8080, Protocol: "http"}}

	applySmartCloudDefaults(&cfg, &current)

	if got := cfg.Deploy.Ports[0]; got.ID != "" || got.Protocol != "TCP" {
		t.Fatalf("expected explicit v2 protocol to remain authoritative, got %#v", got)
	}
}
