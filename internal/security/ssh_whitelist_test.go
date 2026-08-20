package security

import "testing"

// Package managers and the shell must stay out of the ordinary whitelist:
// `trond exec` passes whichever name the caller gives, so allowing them
// globally would hand the SSH user's authority to anyone who can run it.
func TestProvisioningCommandsAreNotGloballyAllowed(t *testing.T) {
	for _, cmd := range []string{"apt-get", "yum", "useradd", "sh"} {
		if err := ValidateCommand(cmd); err == nil {
			t.Errorf("%q must not be in the ordinary whitelist", cmd)
		}
	}
}

// bootstrap needs them, so provisioning mode accepts them — and still
// accepts everything the ordinary whitelist does.
func TestValidateProvisioningCommand(t *testing.T) {
	for _, cmd := range []string{"apt-get", "yum", "useradd", "sh", "docker", "which"} {
		if err := ValidateProvisioningCommand(cmd); err != nil {
			t.Errorf("provisioning should allow %q: %v", cmd, err)
		}
	}
	// Widening is bounded: anything outside both sets is still refused.
	for _, cmd := range []string{"curl", "wget", "nc", "python3"} {
		if err := ValidateProvisioningCommand(cmd); err == nil {
			t.Errorf("provisioning must still refuse %q", cmd)
		}
	}
}
