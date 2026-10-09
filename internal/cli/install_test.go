package cli

import "testing"

func TestMergeNoProxyPreservesExistingAndAddsLoopback(t *testing.T) {
	got := mergeNoProxy("corp.example,localhost", "EXAMPLE.com, corp.example")
	want := "corp.example,localhost,EXAMPLE.com,127.0.0.1,::1"
	if got != want {
		t.Fatalf("mergeNoProxy() = %q, want %q", got, want)
	}
	if !hasLoopbackNoProxy(got) {
		t.Fatalf("hasLoopbackNoProxy(%q) = false", got)
	}
	if hasLoopbackNoProxy("localhost,127.0.0.1") {
		t.Fatal("hasLoopbackNoProxy should require all loopback forms")
	}
}
