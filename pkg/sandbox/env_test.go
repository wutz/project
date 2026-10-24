package sandbox

import "testing"

func TestValidateRejectsBadEnvNames(t *testing.T) {
	base := Form{Name: "agent", Image: "ghcr.io/example/agent:1"}
	for _, env := range [][]EnvVar{
		{{Name: ""}},
		{{Name: "A", Value: "1"}, {Name: "A", Value: "2"}},
	} {
		f := base
		f.Env = env
		if err := f.Validate(); err == nil {
			t.Errorf("Validate accepted env %+v", env)
		}
	}
	f := base
	f.Env = []EnvVar{{Name: "A", Value: "1"}, {Name: "B"}}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate rejected unique env: %v", err)
	}
}
