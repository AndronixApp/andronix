package termux

import "testing"

func TestUnreachableHost(t *testing.T) {
	// support #305 (Play Store Termux, the network refused termux.net)
	out := `Err:1 https://termux.net stable InRelease
  Could not connect to termux.net:443 (203.0.113.1). - connect (111: Connection refused)
  Unable to connect to termux.net:https:
W: Failed to fetch https://termux.net/dists/stable/InRelease  Unable to connect to termux.net:https:`
	if h := unreachableHost(out); h != "termux.net" {
		t.Errorf("got %q", h)
	}
	if h := unreachableHost("Hit:1 https://termux.net stable InRelease\nReading package lists..."); h != "" {
		t.Errorf("a good update: %q", h)
	}
}
