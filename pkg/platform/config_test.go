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
	if st, err := c.StorageType("high-perf"); err != nil || st.StorageClass == "" || st.DefaultAccessMode != "ReadWriteMany" {
		t.Errorf("StorageType(high-perf) = %+v, %v", st, err)
	}
	if _, err := c.StorageType("nope"); err == nil {
		t.Error("unknown storage type accepted")
	}
	if c.Registry.Domain == "" || c.VolumeUsage.BaseURL == "" || c.ImageWarmup.TTL != "2h" {
		t.Errorf("registry=%+v usage=%+v warmup=%+v", c.Registry, c.VolumeUsage, c.ImageWarmup)
	}
	if r, err := c.Resource("xeon6530.xlarge.4c16g"); err != nil || r.UsageFrom == "" {
		t.Errorf("4c16g usageFrom: %+v %v", r, err)
	} else if _, err := c.Resource(r.UsageFrom); err != nil {
		t.Errorf("usageFrom target: %v", err)
	}
	if c.DefaultQueue != "forgeai-queue" || c.ServiceAccount != "pod-default" {
		t.Errorf("unexpected defaults: queue=%q sa=%q", c.DefaultQueue, c.ServiceAccount)
	}
}
