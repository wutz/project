package naming

import "testing"

func TestUserHash(t *testing.T) {
	// Vectors taken from pods created by scc-k8s-v2 in namespace pod-prod.
	cases := []struct{ ns, user, want string }{
		{"pod-prod", "NMPHONE-w16qfDD7qC-4iPXavWNotHc2WBi1wcB9AzovCimGU-s", "97a5af344957"},
		{"pod-prod", "SELF-Y7bM92P1CRvbTeCDmb2jSdzF7KhEQNSPyEeVELNgHGg", "f6dd867476db"},
		{"pod-prod", "public", "public"},
	}
	for _, c := range cases {
		if got := UserHash(c.ns, c.user); got != c.want {
			t.Errorf("UserHash(%q,%q)=%q want %q", c.ns, c.user, got, c.want)
		}
	}
}

func TestFNV1a(t *testing.T) {
	// Docstring example from the Python fnv1a package.
	if got := fnv1a("This is a test.com bro."); got != "ade2f4095d74bf44" {
		t.Fatalf("fnv1a=%s", got)
	}
}

func TestNames(t *testing.T) {
	job := JobName("pod-prod", "SELF-Y7bM92P1CRvbTeCDmb2jSdzF7KhEQNSPyEeVELNgHGg", "ackjob-001")
	if job != "p-f6dd867476db-ackjob-001" {
		t.Fatalf("JobName=%s", job)
	}
	if got := SSHUser("ackjob-001", 1); got != "root@ackjob-001-1" {
		t.Fatalf("SSHUser=%s", got)
	}
}
