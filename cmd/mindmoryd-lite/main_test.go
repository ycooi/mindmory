package main

import "testing"

func TestLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:58080", "localhost:58080", "[::1]:58080"} {
		if !loopbackAddress(address) {
			t.Errorf("rejected loopback address %q", address)
		}
	}
	for _, address := range []string{"0.0.0.0:58080", ":58080", "192.168.1.10:58080", "127.0.0.1", "bad"} {
		if loopbackAddress(address) {
			t.Errorf("accepted non-loopback/invalid address %q", address)
		}
	}
}
