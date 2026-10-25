package sandbox

import (
	"strings"
	"testing"

	"github.com/wutz/forgeai/pkg/naming"
)

func TestRenderTemplateDefaultsAndRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	in := &TemplateForm{
		Name:         "claude-agent",
		Description:  "Agent with the team tools",
		Command:      "npm start",
		Env:          []EnvVar{{Name: "MODE", Value: "dev"}},
		RuntimeClass: "gvisor",
	}
	u, err := RenderTemplate(in, Context{Namespace: "ns", Project: "p", Platform: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if got := u.GetKind(); got != "SandboxTemplate" {
		t.Fatalf("kind = %q, want SandboxTemplate", got)
	}
	if got := u.GetAnnotations()[naming.AnnotationDescription]; got != "Agent with the team tools" {
		t.Errorf("description annotation = %q", got)
	}
	tpl := &SandboxTemplate{}
	if err := FromUnstructured(u.Object, tpl); err != nil {
		t.Fatal(err)
	}
	got := TemplateFormOf(tpl)
	if got.Image != "ghcr.io/example/agent:1" {
		t.Errorf("image = %q, want the platform default", got.Image)
	}
	if got.Command != "npm start" || got.RuntimeClass != "gvisor" {
		t.Errorf("round trip lost command or runtime class: %+v", got)
	}
	if len(got.Env) != 1 || got.Env[0] != (EnvVar{Name: "MODE", Value: "dev"}) {
		t.Errorf("env = %+v", got.Env)
	}
}

func TestRenderTemplateRejectsUnknownRuntimeClass(t *testing.T) {
	_, err := RenderTemplate(&TemplateForm{Name: "t", Image: "img", RuntimeClass: "kata"},
		Context{Platform: testConfig(t)})
	if err == nil {
		t.Fatal("want an error for a runtime class the platform does not offer")
	}
}

func TestRenderRecordsTemplateRef(t *testing.T) {
	u, err := Render(&Form{Name: "sb", Image: "img", Template: "claude-agent"},
		Context{Namespace: "ns", Platform: testConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	if got := u.GetLabels()[LabelTemplateRef]; got != "claude-agent" {
		t.Errorf("template label = %q", got)
	}
}

func TestScriptOfInvertsCommand(t *testing.T) {
	for _, script := range []string{"python agent.py", "set -e\npython agent.py --once"} {
		if got := ScriptOf(command(script)); got != script {
			t.Errorf("ScriptOf(command(%q)) = %q", script, got)
		}
	}
	if got := ScriptOf([]string{"/bin/agent", "--flag"}); got != "/bin/agent --flag" {
		t.Errorf("ScriptOf of a plain argv = %q", got)
	}
}

func TestTemplateFormValidate(t *testing.T) {
	cases := map[string]struct {
		form    TemplateForm
		wantErr string
	}{
		"plain name":          {form: TemplateForm{Name: "claude-agent"}},
		"uppercase name":      {form: TemplateForm{Name: "Agent"}, wantErr: "invalid name"},
		"digit first":         {form: TemplateForm{Name: "1agent"}, wantErr: "invalid name"},
		"name at the limit":   {form: TemplateForm{Name: "a" + strings.Repeat("b", MaxNameLength-1)}},
		"name over the limit": {form: TemplateForm{Name: "a" + strings.Repeat("b", MaxNameLength)}, wantErr: "invalid name"},
		"blank env name":      {form: TemplateForm{Name: "agent", Env: []EnvVar{{Name: " ", Value: "x"}}}, wantErr: "need a name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.form.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}
