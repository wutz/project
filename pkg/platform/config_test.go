package platform

import (
	"os"
	"testing"
)

// The dev overlay config must stay parseable and complete.
func TestParseDevConfig(t *testing.T) {
	data, err := os.ReadFile("../../deploy/dev/platform/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resource("rtx5090.xeon6530.xlarge")
	if err != nil {
		t.Fatal(err)
	}
	if r.GPUCount() != 1 || r.Labels["scclabs.dev/gpu"] != "rtx-5090" {
		t.Errorf("unexpected resource %+v", r)
	}
	if c.SSH.PiperService != "pod-prod/sshpiper" || len(c.SSH.Domains["10.243.147.247"]) != 2 {
		t.Errorf("unexpected ssh config %+v", c.SSH)
	}
	if got := c.Domains(); len(got) != 2 || got[0] != "dev1.bz1.paratera.com" {
		t.Errorf("Domains() = %v", got)
	}
	if c.DefaultQueue != "forgeai-queue" || c.ServiceAccount != "pod-default" {
		t.Errorf("unexpected defaults: queue=%q sa=%q", c.DefaultQueue, c.ServiceAccount)
	}
}
