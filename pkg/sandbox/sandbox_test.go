package sandbox

import (
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/wutz/forgeai/api/v1alpha1"
	"github.com/wutz/forgeai/pkg/naming"
	"github.com/wutz/forgeai/pkg/platform"
)

func testConfig(t *testing.T) *platform.Config {
	t.Helper()
	cfg, err := platform.Parse([]byte(`
sandbox:
  defaultImage: ghcr.io/example/agent:1
  runtimeClasses: [gvisor]
  idleTimeout: 30m
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func instanceType(gpus int32) *v1.InstanceType {
	return &v1.InstanceType{
		ObjectMeta: metav1.ObjectMeta{Name: "rtx-5090.x1"},
		Spec: v1.InstanceTypeSpec{
			GPU:          v1.InstanceTypeGPU{Class: "rtx-5090", Product: "NVIDIA-GeForce-RTX-5090", Count: gpus},
			CPU:          resource.MustParse("15"),
			Memory:       resource.MustParse("120Gi"),
			NodeSelector: map[string]string{naming.LabelGPUProduct: "NVIDIA-GeForce-RTX-5090"},
		},
	}
}

func decode(t *testing.T, u map[string]any) *Sandbox {
	t.Helper()
	sb := &Sandbox{}
	if err := FromUnstructured(u, sb); err != nil {
		t.Fatal(err)
	}
	return sb
}

func TestRenderCPUOnly(t *testing.T) {
	cfg := testConfig(t)
	u, err := Render(&Form{Name: "agent", Image: "ghcr.io/example/agent:1", Service: true},
		Context{Namespace: "forge-demo", Project: "demo", Platform: cfg})
	if err != nil {
		t.Fatal(err)
	}
	sb := decode(t, u.Object)
	if sb.Spec.OperatingMode != OperatingRunning {
		t.Errorf("operatingMode = %q, want Running", sb.Spec.OperatingMode)
	}
	if sb.Spec.Service == nil || !*sb.Spec.Service {
		t.Error("service not requested")
	}
	if got := sb.Labels[naming.LabelProject]; got != "demo" {
		t.Errorf("project label = %q, want demo", got)
	}
	if got := sb.Labels[naming.LabelComponent]; got != naming.ComponentSandbox {
		t.Errorf("component label = %q, want sandbox", got)
	}
	pod := sb.Spec.PodTemplate.Spec
	if len(pod.Containers) != 1 || pod.Containers[0].Name != ContainerName {
		t.Fatalf("containers = %+v, want one named %s", pod.Containers, ContainerName)
	}
	c := pod.Containers[0]
	if c.Image != "ghcr.io/example/agent:1" {
		t.Errorf("image = %q", c.Image)
	}
	// No instance type: the default slice, no GPU, no node placement.
	if got := c.Resources.Requests.Cpu().String(); got != DefaultCPU {
		t.Errorf("cpu = %s, want %s", got, DefaultCPU)
	}
	if _, ok := c.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]; ok {
		t.Error("cpu-only sandbox requested a GPU")
	}
	if pod.NodeSelector != nil {
		t.Errorf("nodeSelector = %v, want none", pod.NodeSelector)
	}
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "gvisor" {
		t.Errorf("runtimeClassName = %v, want the platform default gvisor", pod.RuntimeClassName)
	}
	// /dev/shm is always mounted, next to the volume mounts.
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != "/dev/shm" {
		t.Errorf("volumeMounts = %+v, want /dev/shm", c.VolumeMounts)
	}
}

func TestRenderInstanceTypeVolumesAndTTL(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	u, err := Render(&Form{
		Name:         "trainer",
		Image:        "img",
		Command:      "python agent.py",
		InstanceType: "rtx-5090.x1",
		RuntimeClass: "gvisor",
		Volumes:      []VolumeMount{{Volume: "data", MountPath: "/data"}},
		TTL:          metav1.Duration{Duration: 2 * time.Hour},
	}, Context{Namespace: "forge-demo", Platform: cfg, InstanceType: instanceType(1), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	sb := decode(t, u.Object)
	pod := sb.Spec.PodTemplate.Spec
	c := pod.Containers[0]
	if gpu := c.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]; gpu.Value() != 1 {
		t.Errorf("gpu = %s, want 1", gpu.String())
	}
	if pod.NodeSelector[naming.LabelGPUProduct] == "" {
		t.Error("instance type node selector missing")
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Key != "nvidia.com/gpu" {
		t.Errorf("tolerations = %+v", pod.Tolerations)
	}
	if got := c.Command; len(got) != 3 || got[0] != "/bin/bash" || got[2] != "python agent.py" {
		t.Errorf("command = %v", got)
	}
	// The volume is mounted next to /dev/shm.
	paths := make([]string, 0, len(c.VolumeMounts))
	for _, m := range c.VolumeMounts {
		paths = append(paths, m.MountPath)
	}
	if len(paths) != 2 || paths[1] != "/data" {
		t.Errorf("mounts = %v, want /dev/shm and /data", paths)
	}
	if sb.Spec.ShutdownTime == nil || !sb.Spec.ShutdownTime.Time.Equal(now.Add(2*time.Hour)) {
		t.Errorf("shutdownTime = %v", sb.Spec.ShutdownTime)
	}
	if sb.Spec.ShutdownPolicy == nil || *sb.Spec.ShutdownPolicy != ShutdownRetain {
		t.Errorf("shutdownPolicy = %v, want Retain", sb.Spec.ShutdownPolicy)
	}
	if sb.Annotations[AnnotationTTL] != "2h0m0s" {
		t.Errorf("ttl annotation = %q", sb.Annotations[AnnotationTTL])
	}
	// A form round-trips from the rendered sandbox.
	f := FormOf(sb)
	if f.Image != "img" || f.Command != "python agent.py" || f.RuntimeClass != "gvisor" ||
		f.InstanceType != "rtx-5090.x1" || len(f.Volumes) != 1 || f.Volumes[0].MountPath != "/data" {
		t.Errorf("FormOf = %+v", f)
	}
}

func TestRenderDefaultsFromPlatform(t *testing.T) {
	cfg := testConfig(t)
	u, err := Render(&Form{Name: "agent"}, Context{Namespace: "forge-demo", Platform: cfg})
	if err != nil {
		t.Fatal(err)
	}
	sb := decode(t, u.Object)
	if sb.Spec.PodTemplate.Spec.Containers[0].Image != "ghcr.io/example/agent:1" {
		t.Errorf("image = %q, want the platform default", sb.Spec.PodTemplate.Spec.Containers[0].Image)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]Form{
		"name":         {Name: "Bad_Name", Image: "img"},
		"long name":    {Name: string(make([]byte, 70)), Image: "img"},
		"no image":     {Name: "agent"},
		"bad volume":   {Name: "agent", Image: "img", Volumes: []VolumeMount{{Volume: "data", MountPath: "data"}}},
		"dup mount":    {Name: "agent", Image: "img", Volumes: []VolumeMount{{Volume: "a", MountPath: "/d"}, {Volume: "b", MountPath: "/d"}}},
		"negative ttl": {Name: "agent", Image: "img", TTL: metav1.Duration{Duration: -time.Minute}},
	}
	for name, f := range cases {
		if err := f.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestFormUnmarshalTTL(t *testing.T) {
	cases := map[string]struct {
		data string
		want time.Duration
	}{
		"duration string": {`{"name":"agent","image":"img","ttl":"1h30m0s"}`, 90 * time.Minute},
		"gui model object": {`{"name":"agent","image":"img","ttl":{"Duration":5400000000000}}`,
			90 * time.Minute},
		"no ttl": {`{"name":"agent","image":"img"}`, 0},
	}
	for name, c := range cases {
		var f Form
		if err := json.Unmarshal([]byte(c.data), &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.TTL.Duration != c.want {
			t.Errorf("%s: ttl = %s, want %s", name, f.TTL.Duration, c.want)
		}
		if f.Name != "agent" || f.Image != "img" {
			t.Errorf("%s: lost plain fields: %+v", name, f)
		}
	}
	if err := json.Unmarshal([]byte(`{"ttl":{"Duration":"nope"}}`), &Form{}); err == nil {
		t.Error("expected an error for a non-numeric duration")
	}
}

func TestRuntimeClassAllowList(t *testing.T) {
	cfg := testConfig(t)
	if _, err := Render(&Form{Name: "agent", Image: "img", RuntimeClass: "kata"},
		Context{Platform: cfg, Namespace: "forge-demo"}); err == nil {
		t.Error("expected an error for a runtime class the platform does not offer")
	}
}

func TestPhaseOf(t *testing.T) {
	ready := func(status metav1.ConditionStatus, reason string) *Sandbox {
		return &Sandbox{Status: SandboxStatus{Conditions: []metav1.Condition{
			{Type: ConditionReady, Status: status, Reason: reason, Message: "msg"},
		}}}
	}
	cases := []struct {
		sb   *Sandbox
		want Phase
	}{
		{&Sandbox{}, PhasePending},
		{ready(metav1.ConditionTrue, ReasonDependenciesReady), PhaseRunning},
		{ready(metav1.ConditionFalse, ReasonDependenciesNotReady), PhasePending},
		{ready(metav1.ConditionFalse, ReasonPodFailed), PhaseFailed},
		{ready(metav1.ConditionFalse, ReasonPodSucceeded), PhaseSucceeded},
		{ready(metav1.ConditionFalse, ReasonExpired), PhaseExpired},
		{ready(metav1.ConditionFalse, ReasonSuspended), PhaseStopped},
	}
	for _, c := range cases {
		if got, _ := PhaseOf(c.sb); got != c.want {
			t.Errorf("PhaseOf = %s, want %s", got, c.want)
		}
	}
	// A suspended sandbox reports Stopped even while Ready is still true.
	sb := ready(metav1.ConditionTrue, ReasonDependenciesReady)
	sb.Status.Conditions = append(sb.Status.Conditions,
		metav1.Condition{Type: ConditionSuspended, Status: metav1.ConditionTrue})
	if got, _ := PhaseOf(sb); got != PhaseStopped {
		t.Errorf("PhaseOf(suspended) = %s, want Stopped", got)
	}
}

func TestIdle(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sb := &Sandbox{Status: SandboxStatus{Conditions: []metav1.Condition{
		{Type: ConditionReady, Status: metav1.ConditionTrue},
	}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))}}

	if idle, _ := Idle(sb, pod, 30*time.Minute, now); !idle {
		t.Error("a pod two hours idle should be idle")
	}
	if idle, _ := Idle(sb, pod, 30*time.Minute, now, now.Add(-time.Minute)); idle {
		t.Error("recent activity should keep the sandbox awake")
	}
	if idle, _ := Idle(sb, pod, 0, now); idle {
		t.Error("a zero timeout disables idle detection")
	}
	stopped := &Sandbox{Spec: SandboxSpec{OperatingMode: OperatingSuspended}}
	if idle, _ := Idle(stopped, pod, 30*time.Minute, now); idle {
		t.Error("a stopped sandbox is not idle, it is already stopped")
	}
}
