package naming

import "testing"

func TestGPUClass(t *testing.T) {
	for in, want := range map[string]string{
		"NVIDIA-GeForce-RTX-5090": "rtx-5090",
		"NVIDIA-H100-80GB-HBM3":   "h100-80gb-hbm3",
		"Tesla-V100-SXM2-32GB":    "v100-sxm2-32gb",
		"NVIDIA A800 (PCIe)":      "a800-pcie",
	} {
		if got := GPUClass(in); got != want {
			t.Errorf("GPUClass(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNames(t *testing.T) {
	if got := InstanceTypeName("rtx-5090", 8); got != "rtx-5090.x8" {
		t.Errorf("InstanceTypeName = %q", got)
	}
	if got := InstanceTypeName("a", 16); got != "a.x16" {
		t.Errorf("InstanceTypeName = %q", got)
	}
	if got := Namespace("project-", "demo"); got != "project-demo" {
		t.Errorf("Namespace = %q", got)
	}
}
