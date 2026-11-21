package secureexec

import "testing"

func TestCredentialTransitions(t *testing.T) {
	if credentialsDiffer(10, 10, 20, 20) {
		t.Fatal("unchanged credentials are not a transition")
	}
	if !credentialsDiffer(10, 0, 20, 20) || !credentialsDiffer(10, 10, 20, 0) {
		t.Fatal("credential transition was not detected")
	}
}
