package sandbox

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/wutz/forgeai/pkg/naming"
	"github.com/wutz/forgeai/pkg/platform"
	"github.com/wutz/forgeai/pkg/sshgateway"
)

func sshdContainer(t *testing.T, pod corev1.PodSpec) corev1.Container {
	t.Helper()
	for _, c := range pod.Containers {
		if c.Name == naming.SSHSidecarContainer {
			return c
		}
	}
	t.Fatalf("containers = %+v, want an %s sidecar", pod.Containers, naming.SSHSidecarContainer)
	return corev1.Container{}
}

func TestRenderSSHAddsKeyOnlySidecar(t *testing.T) {
	cfg := testConfig(t)
	u, err := Render(&Form{Name: "agent", Image: "ghcr.io/example/agent:1", SSH: true,
		Volumes: []VolumeMount{{Volume: "data", MountPath: "/data"}}},
		Context{Namespace: "forge-demo", Project: "demo", Platform: cfg})
	if err != nil {
		t.Fatal(err)
	}
	sb := decode(t, u.Object)
	if sb.Labels[naming.LabelSSH] != naming.LiteralTrue {
		t.Errorf("ssh label = %q, want true", sb.Labels[naming.LabelSSH])
	}
	if got := sb.Spec.PodTemplate.ObjectMeta.Labels[naming.LabelSandbox]; got != "agent" {
		t.Errorf("pod label %s = %q, want the sandbox name", naming.LabelSandbox, got)
	}
	pod := sb.Spec.PodTemplate.Spec
	if len(pod.Containers) != 2 || pod.Containers[0].Name != ContainerName {
		t.Fatalf("containers = %+v, want the agent and the sshd sidecar", pod.Containers)
	}

	sshd := sshdContainer(t, pod)
	if sshd.Image != platform.DefaultSSHImage {
		t.Errorf("sshd image = %q, want %q", sshd.Image, platform.DefaultSSHImage)
	}
	if !slices.Equal(sshd.Command, []string{sshgateway.SSHDEntrypoint}) {
		t.Errorf("sshd command = %v, want the entrypoint that stages the keys", sshd.Command)
	}
	args := strings.Join(sshd.Args, " ")
	// The keys are read from the staged directory, not from the Secret mount,
	// whose world-writable directory sshd refuses under StrictModes.
	for _, want := range []string{
		"-o HostKey=" + sshgateway.SSHDHostKeyPath,
		"-o AuthorizedKeysFile=" + sshgateway.SSHDAuthorizedKeys,
		"-o PasswordAuthentication=no",
		"-o KbdInteractiveAuthentication=no",
		"-o AuthenticationMethods=publickey",
		"-o PermitRootLogin=no",
		"-o AllowUsers=" + sshgateway.SandboxUser,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("sshd args = %q, missing %q", args, want)
		}
	}
	if len(sshd.Ports) != 1 || sshd.Ports[0].ContainerPort != SSHPort {
		t.Errorf("sshd ports = %+v, want %d", sshd.Ports, SSHPort)
	}

	// The sidecar mounts the project volume but not /dev/shm, which is the agent's.
	paths := make([]string, 0, len(sshd.VolumeMounts))
	for _, m := range sshd.VolumeMounts {
		paths = append(paths, m.MountPath)
	}
	if !slices.Contains(paths, "/data") || !slices.Contains(paths, sshgateway.SSHDKeysDir) {
		t.Errorf("sshd mounts = %v, want /data and %s", paths, sshgateway.SSHDKeysDir)
	}
	if slices.Contains(paths, "/dev/shm") {
		t.Errorf("sshd mounts = %v, must not mount /dev/shm", paths)
	}
	for _, m := range sshd.VolumeMounts {
		if m.MountPath == sshgateway.SSHDKeysDir && !m.ReadOnly {
			t.Errorf("%s is writable, want read-only", m.MountPath)
		}
	}

	sc := sshd.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("sshd must not allow privilege escalation: %+v", sc)
	}
	if sc == nil || sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("sshd must drop all capabilities first: %+v", sc)
	}
	if sc != nil && sc.Capabilities != nil && slices.Contains(sc.Capabilities.Add, "SYS_ADMIN") {
		t.Errorf("sshd must not add SYS_ADMIN")
	}

	// The keys come from the Secret of the SSH Service, not from the image.
	var keys *corev1.SecretVolumeSource
	for _, v := range pod.Volumes {
		if v.Name == volumeSSHD {
			keys = v.Secret
		}
	}
	if keys == nil {
		t.Fatalf("volumes = %+v, want the %s secret volume", pod.Volumes, volumeSSHD)
	}
	if keys.SecretName != "agent-ssh-hostkey" {
		t.Errorf("secret = %q, want agent-ssh-hostkey", keys.SecretName)
	}
	modes := map[string]int32{}
	for _, it := range keys.Items {
		modes[it.Path] = *it.Mode
	}
	if modes[sshgateway.SSHHostKeyKey] != 0o400 || modes[sshgateway.SSHAuthorizedKeyKey] != 0o444 {
		t.Errorf("key modes = %v, want host key 0400 and authorized_keys 0444", modes)
	}
}

func TestRenderWithoutSSHHasNoSidecar(t *testing.T) {
	u, err := Render(&Form{Name: "agent", Image: "ghcr.io/example/agent:1"},
		Context{Namespace: "forge-demo", Project: "demo", Platform: testConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	sb := decode(t, u.Object)
	if _, ok := sb.Labels[naming.LabelSSH]; ok {
		t.Errorf("ssh label set on a sandbox without SSH")
	}
	if len(sb.Spec.PodTemplate.Spec.Containers) != 1 {
		t.Errorf("containers = %d, want the agent only", len(sb.Spec.PodTemplate.Spec.Containers))
	}
	if sb.Spec.PodTemplate.ObjectMeta.Labels != nil {
		t.Errorf("pod labels = %v, want none", sb.Spec.PodTemplate.ObjectMeta.Labels)
	}
}

func TestRenderSSHUsesPlatformSidecarImage(t *testing.T) {
	cfg, err := platform.Parse([]byte(`
sandbox:
  sshImage: registry.example/forgeai-sshd:9
`))
	if err != nil {
		t.Fatal(err)
	}
	u, err := Render(&Form{Name: "agent", Image: "img:1", SSH: true}, Context{Namespace: "ns", Platform: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if got := sshdContainer(t, decode(t, u.Object).Spec.PodTemplate.Spec).Image; got != "registry.example/forgeai-sshd:9" {
		t.Errorf("sshd image = %q, want the platform's", got)
	}
}

func TestValidateSSHNameLength(t *testing.T) {
	long := strings.Repeat("a", MaxSSHNameLength)
	tooLong := strings.Repeat("a", MaxSSHNameLength+1)
	tests := []struct {
		name    string
		form    Form
		wantErr bool
	}{
		{name: "ssh name at the limit", form: Form{Name: long, Image: "i", SSH: true}},
		{name: "ssh name over the limit", form: Form{Name: tooLong, Image: "i", SSH: true}, wantErr: true},
		{name: "plain name over the ssh limit", form: Form{Name: tooLong, Image: "i"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.form.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFormOfRoundTripsSSH(t *testing.T) {
	u, err := Render(&Form{Name: "agent", Image: "img:1", SSH: true}, Context{Namespace: "ns"})
	if err != nil {
		t.Fatal(err)
	}
	if got := FormOf(decode(t, u.Object)); !got.SSH {
		t.Errorf("FormOf().SSH = false, want true")
	}
}
