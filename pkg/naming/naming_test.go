package naming

import "testing"

const (
	testNS   = "pod-prod"
	testUser = "SELF-Y7bM92P1CRvbTeCDmb2jSdzF7KhEQNSPyEeVELNgHGg"
)

func TestUserHash(t *testing.T) {
	// Vectors taken from pods created by scc-k8s-v2 in namespace pod-prod.
	cases := []struct{ ns, user, want string }{
		{testNS, "NMPHONE-w16qfDD7qC-4iPXavWNotHc2WBi1wcB9AzovCimGU-s", "97a5af344957"},
		{testNS, "SELF-Y7bM92P1CRvbTeCDmb2jSdzF7KhEQNSPyEeVELNgHGg", "f6dd867476db"},
		{testNS, "public", "public"},
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
	job := JobName(testNS, "SELF-Y7bM92P1CRvbTeCDmb2jSdzF7KhEQNSPyEeVELNgHGg", "ackjob-001")
	if job != "p-f6dd867476db-ackjob-001" {
		t.Fatalf("JobName=%s", job)
	}
	if got := VolumeClaimName(testNS, testUser, "vol-1"); got != "p-f6dd867476db-vol-1" {
		t.Fatalf("VolumeClaimName=%s", got)
	}
	if a, b := ImageWarmupName("img:1"), ImageWarmupName("img:2"); a == b || len(a) != len("forgeai-warmup-")+16 {
		t.Fatalf("ImageWarmupName=%s %s", a, b)
	}
}
