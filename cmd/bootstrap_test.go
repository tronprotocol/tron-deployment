package cmd

import "testing"

// bootstrap has to be re-runnable, so a useradd that fails only because
// the user is already there must not abort the run — but anything else
// must, now that provisioning mode lets the call actually execute.
func TestUserAlreadyExists(t *testing.T) {
	tolerated := []string{
		"useradd: user 'tron' already exists",
		"useradd: UID 999 is not unique\nuseradd: name tron already in use",
		"ALREADY EXISTS",
	}
	for _, out := range tolerated {
		if !userAlreadyExists([]byte(out)) {
			t.Errorf("should tolerate: %q", out)
		}
	}

	fatal := []string{
		"useradd: Permission denied.",
		"useradd: cannot open /etc/passwd",
		"useradd: invalid shell '/usr/sbin/nologin'",
		"",
	}
	for _, out := range fatal {
		if userAlreadyExists([]byte(out)) {
			t.Errorf("should not tolerate: %q", out)
		}
	}
}
