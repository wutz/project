package platform

import (
	"os"
	"testing"
	"time"
)

func TestParseDevConfig(t *testing.T) {
	data, err := os.ReadFile("../../deploy/dev/platform/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.NamespacePrefix != "forge-" {
		t.Errorf("prefix = %q", c.NamespacePrefix)
	}
	if got := c.Ingress.URL("files-forge-a"); got != "https://files-forge-a.dev1.bz1.paratera.com:7443" {
		t.Errorf("ingress URL = %q", got)
	}
	if _, st, err := c.StorageType(""); err != nil || st.AccessMode != "ReadWriteMany" {
		t.Errorf("default storage type: %+v %v", st, err)
	}
	if !c.Registry.Enabled() || c.Registry.APIURL() != "https://cr.dev1.bz1.paratera.com:7443" {
		t.Errorf("registry = %+v", c.Registry)
	}
}

func TestDefaults(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Ingress.URL("x") != "https://x." {
		t.Errorf("port 443 must be omitted: %s", c.Ingress.URL("x"))
	}
	if c.DefaultQuota.GPUCount("any") != 8 || c.FileServer.IdleTimeout.Duration != 30*time.Minute {
		t.Errorf("defaults: %+v", c.DefaultQuota)
	}
	if c.InstanceTypes.Reserved.CPU.String() != "2" || c.TrashRetention.Duration != 7*24*time.Hour {
		t.Errorf("defaults: %+v", c.InstanceTypes)
	}
	if _, _, err := c.StorageType("nope"); err == nil {
		t.Error("expected unknown storage type error")
	}
}

func TestGatewayURL(t *testing.T) {
	for _, tc := range []struct {
		scheme string
		port   int
		want   string
	}{
		{"http", 8080, "http://web.example.com:8080"},
		{"http", 80, "http://web.example.com"},
		{"https", 443, "https://web.example.com"},
		{"https", 8443, "https://web.example.com:8443"},
	} {
		g := Gateway{Name: "gw", Scheme: tc.scheme, Port: tc.port}
		if got := g.URL("web.example.com"); got != tc.want {
			t.Errorf("URL(%s:%d) = %q, want %q", tc.scheme, tc.port, got, tc.want)
		}
	}
}
