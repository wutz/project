package sandbox

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/wutz/forgeai/pkg/naming"
	"github.com/wutz/forgeai/pkg/platform"
)

func gatewayConfig(t *testing.T) *platform.Config {
	t.Helper()
	cfg, err := platform.Parse([]byte(`
ingress: {domain: example.com, port: 7443}
gateway: {name: forgeai-sandbox, namespace: forgeai-system, scheme: http, port: 8080}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCheckGateway(t *testing.T) {
	cfg := gatewayConfig(t)
	if err := CheckGateway("web", "forge-agents", cfg); err != nil {
		t.Fatalf("CheckGateway: %v", err)
	}

	noGateway, err := platform.Parse([]byte(`ingress: {domain: example.com}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckGateway("web", "forge-agents", noGateway); err == nil {
		t.Error("CheckGateway without a configured gateway: no error")
	}

	// The host <name>-<namespace> must fit a DNS label of 63 characters.
	if err := CheckGateway(strings.Repeat("a", 50), "forge-agents", cfg); err != nil {
		t.Errorf("host of 63 characters: %v", err)
	}
	if err := CheckGateway(strings.Repeat("a", 51), "forge-agents", cfg); err == nil {
		t.Error("host of 64 characters: no error")
	}
	// The Service <name>-gw must fit one too.
	if err := CheckGateway(strings.Repeat("a", 60), "a", cfg); err != nil {
		t.Errorf("service of 63 characters: %v", err)
	}
	if err := CheckGateway(strings.Repeat("a", 61), "a", cfg); err == nil {
		t.Error("service of 64 characters: no error")
	}
}

func TestGatewayHost(t *testing.T) {
	ingress := platform.Ingress{Domain: "dev1.example.com"}
	if got := GatewayHost("web", "forge-agents", ingress); got != "web-forge-agents.dev1.example.com" {
		t.Errorf("GatewayHost = %q", got)
	}
}

func TestGatewayRouteAndService(t *testing.T) {
	cfg := gatewayConfig(t)
	sb := &Sandbox{ObjectMeta: metav1.ObjectMeta{
		Name: "web", Namespace: "forge-agents",
		Labels: map[string]string{naming.LabelProject: "agents"},
	}}

	route := GatewayRoute(sb, GatewayHost(sb.Name, sb.Namespace, cfg.Ingress), 8080, cfg.Gateway)
	if route.GetKind() != "HTTPRoute" || route.GetAPIVersion() != "gateway.networking.k8s.io/v1" {
		t.Errorf("route kind = %s %s", route.GetAPIVersion(), route.GetKind())
	}
	if route.GetName() != "web" || route.GetNamespace() != "forge-agents" {
		t.Errorf("route = %s/%s", route.GetNamespace(), route.GetName())
	}
	if got := route.GetLabels()[naming.LabelProject]; got != "agents" {
		t.Errorf("route project label = %q", got)
	}
	parents := nestedMaps(route.Object, "spec", "parentRefs")
	if len(parents) != 1 || parents[0]["name"] != "forgeai-sandbox" || parents[0]["namespace"] != "forgeai-system" {
		t.Errorf("parentRefs = %v", parents)
	}
	hosts, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if len(hosts) != 1 || hosts[0] != "web-forge-agents.example.com" {
		t.Errorf("hostnames = %v", hosts)
	}
	rules := nestedMaps(route.Object, "spec", "rules")
	if len(rules) != 1 {
		t.Fatalf("rules = %v", rules)
	}
	backends, _ := rules[0]["backendRefs"].([]any)
	if len(backends) != 1 {
		t.Fatalf("backendRefs = %v", backends)
	}
	backend, _ := backends[0].(map[string]any)
	if backend["name"] != "web-gw" || backend["port"] != int64(8080) {
		t.Errorf("backendRef = %v", backend)
	}

	svc := GatewayService(sb, map[string]string{LabelSandboxNameHash: "abc"}, 8080)
	if svc.Name != "web-gw" || svc.Spec.Selector[LabelSandboxNameHash] != "abc" {
		t.Errorf("service = %s selector %v", svc.Name, svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8080 || svc.Spec.Ports[0].TargetPort.IntValue() != 8080 {
		t.Errorf("service ports = %v", svc.Spec.Ports)
	}
	if svc.Labels[naming.LabelManagedBy] != naming.ManagedBy {
		t.Errorf("service is not labelled as managed: %v", svc.Labels)
	}
}

// nestedMaps returns the maps of a list field of an unstructured object.
func nestedMaps(obj map[string]any, fields ...string) []map[string]any {
	list, _, _ := unstructured.NestedSlice(obj, fields...)
	out := make([]map[string]any, 0, len(list))
	for _, v := range list {
		m, _ := v.(map[string]any)
		out = append(out, m)
	}
	return out
}

func TestGatewayPortOf(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"8080", 8080},
		{"0", 0},
		{"65536", 0},
		{"http", 0},
		{"", 0},
	} {
		sb := &Sandbox{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			naming.AnnotationGatewayPort: tc.value,
		}}}
		if got := GatewayPortOf(sb); got != tc.want {
			t.Errorf("GatewayPortOf(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestValidateGatewayPort(t *testing.T) {
	if err := (&Form{Name: "web", Image: "busybox", GatewayPort: 65535}).Validate(); err != nil {
		t.Errorf("port 65535: %v", err)
	}
	if err := (&Form{Name: "web", Image: "busybox", GatewayPort: -1}).Validate(); err == nil {
		t.Error("port -1: no error")
	}
	if err := (&Form{Name: "web", Image: "busybox", GatewayPort: 65536}).Validate(); err == nil {
		t.Error("port 65536: no error")
	}
}
