package sandbox

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase is the state of a sandbox as shown in the GUI and used by the
// operator's idle detection.
type Phase string

const (
	// PhaseRunning means the sandbox is up and Ready.
	PhaseRunning Phase = "Running"
	// PhasePending means the pod exists but is not ready yet.
	PhasePending Phase = "Pending"
	// PhaseStopped means the sandbox is suspended (no pod).
	PhaseStopped Phase = "Stopped"
	// PhaseSucceeded means the workload finished on its own.
	PhaseSucceeded Phase = "Succeeded"
	// PhaseFailed means the sandbox cannot run (pod failed, bad spec, ...).
	PhaseFailed Phase = "Failed"
	// PhaseExpired means the sandbox reached its shutdownTime.
	PhaseExpired Phase = "Expired"
)

// Condition returns the condition of a type, or nil.
func Condition(sb *Sandbox, t string) *metav1.Condition {
	for i := range sb.Status.Conditions {
		if sb.Status.Conditions[i].Type == t {
			return &sb.Status.Conditions[i]
		}
	}
	return nil
}

// PhaseOf derives the phase from the sandbox status conditions.
func PhaseOf(sb *Sandbox) (Phase, string) {
	if sb == nil {
		return PhasePending, ""
	}
	if c := Condition(sb, ConditionSuspended); c != nil && c.Status == metav1.ConditionTrue {
		return PhaseStopped, c.Message
	}
	ready := Condition(sb, ConditionReady)
	switch {
	case ready == nil:
		if sb.Status.ServiceFQDN == "" && sb.Status.NodeName == "" && len(sb.Status.PodIPs) == 0 {
			return PhasePending, "Waiting for the sandbox controller"
		}
		return PhasePending, ""
	case ready.Status == metav1.ConditionTrue:
		return PhaseRunning, ready.Message
	}
	switch ready.Reason {
	case ReasonPodSucceeded:
		return PhaseSucceeded, ready.Message
	case ReasonPodFailed, ReasonInvalidConfiguration, ReasonReconcilerError, ReasonMultiplePods:
		return PhaseFailed, ready.Message
	case ReasonExpired:
		return PhaseExpired, ready.Message
	case ReasonSuspended:
		return PhaseStopped, ready.Message
	default:
		return PhasePending, ready.Message
	}
}

// PodName is the backing pod of a sandbox: its own name, unless a legacy
// annotation names an adopted warm-pool pod.
func PodName(sb *Sandbox) string {
	if n := sb.Annotations[AnnotationPodName]; n != "" {
		return n
	}
	return sb.Name
}

// Idle reports whether nothing has used the sandbox since the last activity
// and returns how long that has been. Activity is the later of the pod's
// start time and the newest of the passed timestamps (CPU usage samples,
// file access times). A sandbox without a running pod is not idle: it is
// stopped or starting, and stopping is the caller's decision.
func Idle(sb *Sandbox, pod *corev1.Pod, timeout time.Duration, now time.Time,
	activity ...time.Time) (bool, time.Duration) {
	if timeout <= 0 || pod == nil {
		return false, 0
	}
	if phase, _ := PhaseOf(sb); phase != PhaseRunning {
		return false, 0
	}
	last := pod.CreationTimestamp.Time
	if pod.Status.StartTime != nil && pod.Status.StartTime.After(last) {
		last = pod.Status.StartTime.Time
	}
	for _, t := range activity {
		if t.After(last) {
			last = t
		}
	}
	if now.Sub(last) >= timeout {
		return true, now.Sub(last)
	}
	return false, 0
}

// ExecutableCommand is the command of the sandbox container, for the GUI.
func ExecutableCommand(sb *Sandbox) []string {
	for _, c := range sb.Spec.PodTemplate.Spec.Containers {
		if c.Name == ContainerName {
			if len(c.Command) > 0 {
				return append(c.Command, c.Args...)
			}
			return nil
		}
	}
	return nil
}

// TrimMessages collapses the newlines some controller messages carry.
func TrimMessages(s string) string { return strings.Join(strings.Fields(s), " ") }
