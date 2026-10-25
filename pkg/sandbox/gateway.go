package sandbox

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/wutz/forgeai/pkg/naming"
	"github.com/wutz/forgeai/pkg/platform"
)

// gatewaySuffix names the Service that fronts a published sandbox. The
// agent-sandbox Service is headless and has no ports, so a Gateway route can
// only point at a Service forgeai creates.
const gatewaySuffix = "-gw"

// GatewayServiceName is the Service that routes to a published sandbox.
func GatewayServiceName(name string) string { return name + gatewaySuffix }

// GatewayHost is the host name of a published sandbox: <name>-<namespace> under
// the ingress domain, so one wildcard DNS entry serves every sandbox.
func GatewayHost(name, namespace string, ingress platform.Ingress) string {
	return ingress.Host(name + "-" + namespace)
}

// CheckGateway reports whether a sandbox can be published through the platform
// gateway: the gateway is configured, and the host and Service names fit a DNS
// label.
func CheckGateway(name, namespace string, cfg *platform.Config) error {
	if !cfg.Gateway.Enabled() {
		return fmt.Errorf("gateway access is not configured on the platform")
	}
	if l := len(name) + len(namespace) + 1; l > 63 {
		return fmt.Errorf("sandbox %q is too long to publish through the gateway: its name and namespace must fit in 63 characters together, got %d", name, l)
	}
	if l := len(GatewayServiceName(name)); l > 63 {
		return fmt.Errorf("sandbox %q is too long to publish through the gateway: at most %d characters", name, 63-len(gatewaySuffix))
	}
	return nil
}

// GatewayPortOf is the port a sandbox publishes through the gateway, or 0.
func GatewayPortOf(sb *Sandbox) int {
	n, err := strconv.Atoi(sb.Annotations[naming.AnnotationGatewayPort])
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// GatewayService is the ClusterIP Service that selects the pod of a published
// sandbox on its port, owned by the sandbox.
func GatewayService(sb *Sandbox, selector map[string]string, port int) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name: GatewayServiceName(sb.Name), Namespace: sb.Namespace,
			Labels: gatewayLabels(sb),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports: []corev1.ServicePort{{
				Name: "http", Protocol: corev1.ProtocolTCP,
				Port: int32(port), TargetPort: intstr.FromInt32(int32(port)),
			}},
		},
	}
}

// GatewayRoute is the HTTPRoute that publishes a sandbox on its host name
// through the platform gateway, to the sandbox's gateway Service.
func GatewayRoute(sb *Sandbox, host string, port int, gw platform.Gateway) *unstructured.Unstructured {
	parent := map[string]any{"name": gw.Name, "namespace": gw.Namespace}
	if gw.SectionName != "" {
		parent["sectionName"] = gw.SectionName
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": sb.Name, "namespace": sb.Namespace},
		"spec": map[string]any{
			"parentRefs": []any{parent},
			"hostnames":  []any{host},
			"rules": []any{map[string]any{
				"backendRefs": []any{map[string]any{
					"name": GatewayServiceName(sb.Name), "port": int64(port),
				}},
			}},
		},
	}}
	u.SetGroupVersionKind(HTTPRouteGVK)
	// SetLabels, not a Go map literal: unstructured objects hold JSON types only.
	u.SetLabels(gatewayLabels(sb))
	return u
}

func gatewayLabels(sb *Sandbox) map[string]string {
	labels := map[string]string{
		naming.LabelManagedBy: naming.ManagedBy,
		naming.LabelComponent: naming.ComponentSandbox,
	}
	if p := sb.Labels[naming.LabelProject]; p != "" {
		labels[naming.LabelProject] = p
	}
	return labels
}
