package apply

import "testing"

func TestEndpointHelpers(t *testing.T) {
	if got := PortOrDefault(0, 8090); got != 8090 {
		t.Fatalf("default=%d", got)
	}
	if got := PortOrDefault(9000, 8090); got != 9000 {
		t.Fatalf("stored=%d", got)
	}
	if got := HTTPURL("host", 8090); got != "http://host:8090" {
		t.Fatalf("http=%q", got)
	}
	if got := GRPCAddr("host", 50051); got != "host:50051" {
		t.Fatalf("grpc=%q", got)
	}
	if got := ProbeURL(0, "/wallet/getnowblock"); got != "http://127.0.0.1:8090/wallet/getnowblock" {
		t.Fatalf("probe=%q", got)
	}
}
