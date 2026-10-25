package sandbox

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// withContainer returns a sandbox whose sandbox container has the given command.
func withContainer(command, args []string) *Sandbox {
	sb := &Sandbox{}
	sb.Spec.PodTemplate.Spec.Containers = []corev1.Container{
		{Name: "sidecar", Command: []string{"ignored"}},
		{Name: ContainerName, Command: command, Args: args},
	}
	return sb
}

func TestExecutableCommand(t *testing.T) {
	if got := ExecutableCommand(withContainer([]string{"sh", "-c"}, []string{"echo hi"})); !slices.Equal(got, []string{"sh", "-c", "echo hi"}) {
		t.Errorf("command %v, want sh -c 'echo hi'", got)
	}
	if got := ExecutableCommand(withContainer(nil, []string{"orphan"})); got != nil {
		t.Errorf("args without a command gave %v, want nil", got)
	}
	if got := ExecutableCommand(&Sandbox{}); got != nil {
		t.Errorf("no containers gave %v, want nil", got)
	}
}

// ExecutableCommand's result must not share the spare capacity of the
// container's command: appending to it would write into the spec.
func TestExecutableCommandDoesNotAliasTheSpec(t *testing.T) {
	command := make([]string, 1, 4)
	command[0] = "sleep"
	sb := withContainer(command, []string{"1d"})

	ExecutableCommand(sb)

	spec := sb.Spec.PodTemplate.Spec.Containers[1].Command
	if len(spec) != 1 || spec[0] != "sleep" {
		t.Errorf("spec command changed to %v", spec)
	}
	if full := spec[:cap(spec)]; full[1] != "" {
		t.Errorf("spare capacity of the spec command written to: %q", full)
	}
}

func TestPodName(t *testing.T) {
	sb := &Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "agent"}}
	if got := PodName(sb); got != "agent" {
		t.Errorf("pod name %q, want the sandbox name", got)
	}
	sb.Annotations = map[string]string{AnnotationPodName: "warm-7"}
	if got := PodName(sb); got != "warm-7" {
		t.Errorf("pod name %q, want the adopted warm pod", got)
	}
}

func TestTrimMessages(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"  one  line ":           "one line",
		"first\nsecond\n\tthird": "first second third",
	} {
		if got := TrimMessages(in); got != want {
			t.Errorf("TrimMessages(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPhaseOfWithoutStatus(t *testing.T) {
	if phase, msg := PhaseOf(nil); phase != PhasePending || msg != "" {
		t.Errorf("PhaseOf(nil) = %s %q, want Pending", phase, msg)
	}
	if phase, _ := PhaseOf(&Sandbox{}); phase != PhasePending {
		t.Errorf("PhaseOf(no status) = %s, want Pending", phase)
	}
}

func TestPhaseOfReportsReadyMessage(t *testing.T) {
	sb := &Sandbox{Status: SandboxStatus{Conditions: []metav1.Condition{
		{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: ReasonPodFailed, Message: "image pull failed"},
	}}}
	phase, msg := PhaseOf(sb)
	if phase != PhaseFailed || msg != "image pull failed" {
		t.Errorf("PhaseOf = %s %q, want Failed with the controller message", phase, msg)
	}
}

func TestPhaseOfSuspendedWithoutReadyCondition(t *testing.T) {
	sb := &Sandbox{Status: SandboxStatus{Conditions: []metav1.Condition{
		{Type: ConditionSuspended, Status: metav1.ConditionTrue, Message: "idle timeout"},
	}}}
	phase, msg := PhaseOf(sb)
	if phase != PhaseStopped || msg != "idle timeout" {
		t.Errorf("PhaseOf = %s %q, want Stopped with the suspend message", phase, msg)
	}
}

func TestPhaseReasonBeforeControllerReports(t *testing.T) {
	if got := PhaseReason(nil); got != ReasonAwaitingController {
		t.Errorf("PhaseReason(nil) = %q, want %q", got, ReasonAwaitingController)
	}
	if got := PhaseReason(&Sandbox{}); got != ReasonAwaitingController {
		t.Errorf("PhaseReason(empty) = %q, want %q", got, ReasonAwaitingController)
	}
	// Once the controller has placed the sandbox, it is pending rather than awaited.
	placed := &Sandbox{Status: SandboxStatus{ServiceFQDN: "agent.forge-agents.svc"}}
	if got := PhaseReason(placed); got != "" {
		t.Errorf("PhaseReason(placed, no Ready yet) = %q, want empty", got)
	}
}

func TestIsSandboxPaused(t *testing.T) {
	if IsSandboxPaused(&Sandbox{}) {
		t.Error("a new sandbox reads as paused")
	}
	sb := &Sandbox{}
	sb.Spec.OperatingMode = OperatingSuspended
	if !IsSandboxPaused(sb) {
		t.Error("a suspended operating mode does not read as paused")
	}
	sb = &Sandbox{Status: SandboxStatus{Conditions: []metav1.Condition{
		{Type: ConditionSuspended, Status: metav1.ConditionTrue},
	}}}
	if !IsSandboxPaused(sb) {
		t.Error("a Suspended condition does not read as paused")
	}
	sb.Status.Conditions[0].Status = metav1.ConditionFalse
	if IsSandboxPaused(sb) {
		t.Error("a cleared Suspended condition still reads as paused")
	}
}

func TestIdleNeedsRunningSandboxAndTimeout(t *testing.T) {
	now := time.Now()
	running := &Sandbox{Status: SandboxStatus{Conditions: []metav1.Condition{
		{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonDependenciesReady},
	}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}

	if idle, _ := Idle(running, nil, time.Minute, now); idle {
		t.Error("a sandbox without a pod is idle")
	}
	if idle, _ := Idle(running, pod, 0, now); idle {
		t.Error("a zero timeout still reports idle")
	}
	if idle, _ := Idle(&Sandbox{}, pod, time.Minute, now); idle {
		t.Error("a pending sandbox is idle")
	}
	if idle, _ := Idle(running, pod, time.Minute, now, now.Add(-time.Second)); idle {
		t.Error("a recent activity sample still reports idle")
	}
}
