package instancetype

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/wutz/forgeai/pkg/platform"
)

func gpuNode(name string, ready bool) corev1.Node {
	status := corev1.ConditionTrue
	if !ready {
		status = corev1.ConditionFalse
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-5090", "nvidia.com/gpu.memory": "32607",
		}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				"cpu": resource.MustParse("127400m"), "memory": resource.MustParse("1055574816Ki"), ResourceGPU: resource.MustParse("8"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
		},
	}
}

func pod(node string, cpu, mem string, gpus int64) corev1.Pod {
	req := corev1.ResourceList{"cpu": resource.MustParse(cpu), "memory": resource.MustParse(mem)}
	if gpus > 0 {
		req[ResourceGPU] = *resource.NewQuantity(gpus, resource.DecimalSI)
	}
	return corev1.Pod{
		Spec:   corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: req}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func cfg() platform.InstanceTypes {
	c, _ := platform.Parse([]byte("instanceTypes: {reserved: {cpu: '8', memory: 64Gi}, sizes: [1, 2, 4, 8, 16]}"))
	return c.InstanceTypes
}

func TestCompute(t *testing.T) {
	nodes := []corev1.Node{gpuNode("gn-1", true), gpuNode("gn-2", true), gpuNode("gn-3", false),
		{ObjectMeta: metav1.ObjectMeta{Name: "cpu-only"}}}
	pods := []corev1.Pod{
		pod("gn-1", "28", "234Gi", 2), // a 2-GPU job
		pod("gn-1", "2", "4Gi", 0),    // system daemons fit in the reservation
		pod("gn-2", "1", "1Gi", 0),
		pod("cpu-only", "1", "1Gi", 0),
	}
	classes, types := Compute(nodes, pods, cfg(), metav1.Now())
	if len(classes) != 1 {
		t.Fatalf("classes = %+v", classes)
	}
	c := classes[0]
	// (127.4 - 8) / 8 = 14.9 -> 14 cores; (1006.7Gi - 64Gi) / 8 = 117.8 -> 117Gi.
	if c.Name != "rtx-5090" || c.MilliCPU != 14000 || c.Memory != 117<<30 || c.MaxGPUs != 8 || c.MemoryMiB != 32607 {
		t.Fatalf("class = %+v", c)
	}
	// 16 is larger than a node and skipped.
	if len(types) != 4 {
		t.Fatalf("types = %d", len(types))
	}
	x1, x8 := types[0], types[3]
	if x1.Name != "rtx-5090.x1" || x1.Spec.CPU.String() != "14" || x1.Spec.Memory.String() != "117Gi" {
		t.Errorf("x1 spec = %+v", x1.Spec)
	}
	if x8.Name != "rtx-5090.x8" || x8.Spec.CPU.String() != "112" || x8.Spec.NodeSelector["nvidia.com/gpu.product"] != "NVIDIA-GeForce-RTX-5090" {
		t.Errorf("x8 spec = %+v", x8.Spec)
	}
	// gn-3 is NotReady: no capacity. gn-1 has 6 free GPUs, gn-2 has 8.
	if x1.Status.Total != 16 || x1.Status.Free != 14 {
		t.Errorf("x1 status = %+v", x1.Status)
	}
	if x8.Status.Total != 2 || x8.Status.Free != 1 {
		t.Errorf("x8 status = %+v", x8.Status)
	}
	if x8.Status.Nodes[2].Node != "gn-3" || x8.Status.Nodes[2].Total != 0 {
		t.Errorf("x8 nodes = %+v", x8.Status.Nodes)
	}
}

func TestFreeLimitedByCPU(t *testing.T) {
	// A CPU-heavy non-GPU pod beyond the reservation eats instance capacity.
	pods := []corev1.Pod{pod("gn-1", "60", "1Gi", 0)}
	_, types := Compute([]corev1.Node{gpuNode("gn-1", true)}, pods, cfg(), metav1.Now())
	// usable 119.4 - (60-8) = 67.4 cores -> 4 x1 instances of 14 cores.
	if types[0].Status.Free != 4 {
		t.Errorf("x1 free = %d", types[0].Status.Free)
	}
}
