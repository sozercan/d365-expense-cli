package session

import "testing"

func TestSessionStatusValidationAndApplyBootstrapPreservesStatus(t *testing.T) {
	profile := validBootstrapProfile()
	stored, err := FromBootstrap(profile)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusReady {
		t.Fatalf("status = %q, want ready", stored.Status)
	}
	stored.Status = StatusUncertain
	if err := stored.Validate(); err != nil {
		t.Fatalf("uncertain session rejected: %v", err)
	}
	if err := stored.ApplyBootstrap(profile); err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusUncertain {
		t.Fatalf("ApplyBootstrap status = %q, want uncertain", stored.Status)
	}
	stored.Status = "invalid"
	if err := stored.Validate(); err == nil {
		t.Fatal("invalid status accepted")
	}
}
