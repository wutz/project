package harbor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/wutz/project/pkg/harbor"
	"github.com/wutz/project/pkg/harbor/harbortest"
)

func TestProjectLifecycle(t *testing.T) {
	ctx := context.Background()
	srv := harbortest.New("admin", "pw")
	defer srv.Close()
	c := harbor.New(srv.URL, "admin", "pw", true)

	p, err := c.EnsureProject(ctx, "forge-demo")
	if err != nil {
		t.Fatal(err)
	}
	if p2, err := c.EnsureProject(ctx, "forge-demo"); err != nil || p2.ID != p.ID {
		t.Fatalf("EnsureProject not idempotent: %v %v", p2, err)
	}
	if err := c.SetStorageQuota(ctx, p, 10<<30); err != nil || srv.Quotas[p.ID] != 10<<30 {
		t.Fatalf("quota: %v %d", err, srv.Quotas[p.ID])
	}

	secret, err := harbor.GenerateSecret()
	if err != nil || len(secret) != 32 {
		t.Fatalf("secret %q %v", secret, err)
	}
	name, err := c.EnsureRobot(ctx, p, harbor.RobotName, secret)
	if err != nil || name != "robot$forge-demo+forgeai" || srv.RobotSecret(name) != secret {
		t.Fatalf("robot %q %v", name, err)
	}
	// A lost secret is reset on the existing robot.
	if _, err := c.EnsureRobot(ctx, p, harbor.RobotName, "Other1234"); err != nil || srv.RobotSecret(name) != "Other1234" {
		t.Fatalf("reset robot secret: %v", err)
	}

	// The robot lists and deletes images.
	srv.AddArtifact("forge-demo", "team/train", "sha256:a", "v1", "latest")
	srv.AddArtifact("forge-demo", "team/train", "sha256:b", "v2")
	robot := harbor.New(srv.URL, name, "Other1234", true)
	repos, err := robot.ListRepositories(ctx, "forge-demo")
	if err != nil || len(repos) != 1 || repos[0].Name != "team/train" {
		t.Fatalf("repos %+v %v", repos, err)
	}
	arts, err := robot.ListArtifacts(ctx, "forge-demo", "team/train")
	if err != nil || len(arts) != 2 || arts[0].TagNames()[1] != "latest" {
		t.Fatalf("artifacts %+v %v", arts, err)
	}
	if err := robot.DeleteArtifact(ctx, "forge-demo", "team/train", "sha256:a"); err != nil {
		t.Fatal(err)
	}
	if arts, _ := robot.ListArtifacts(ctx, "forge-demo", "team/train"); len(arts) != 1 {
		t.Fatalf("after delete %+v", arts)
	}

	if err := c.DeleteProject(ctx, "forge-demo"); err != nil || srv.HasProject("forge-demo") {
		t.Fatalf("delete project: %v", err)
	}
	if err := c.DeleteProject(ctx, "forge-demo"); err != nil {
		t.Fatalf("delete missing project: %v", err)
	}
	if _, err := c.GetProject(ctx, "forge-demo"); !errors.Is(err, harbor.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestHasRobot(t *testing.T) {
	ctx := context.Background()
	srv := harbortest.New("admin", "pw")
	defer srv.Close()
	c := harbor.New(srv.URL, "admin", "pw", true)

	p, err := c.EnsureProject(ctx, "forge-demo")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.HasRobot(ctx, p, harbor.RobotName); err != nil || ok {
		t.Fatalf("robot before creation: %v %v", ok, err)
	}
	name, err := c.EnsureRobot(ctx, p, harbor.RobotName, "Secret1234")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.HasRobot(ctx, p, harbor.RobotName); err != nil || !ok {
		t.Fatalf("robot after creation: %v %v", ok, err)
	}
	// Removing the robot in Harbor is seen, and EnsureRobot issues it again.
	srv.RemoveRobot(name)
	if ok, err := c.HasRobot(ctx, p, harbor.RobotName); err != nil || ok {
		t.Fatalf("robot after removal: %v %v", ok, err)
	}
	if _, err := c.EnsureRobot(ctx, p, harbor.RobotName, "Secret5678"); err != nil || srv.RobotSecret(name) != "Secret5678" {
		t.Fatalf("reissue robot: %v", err)
	}
}

func TestDockerConfigJSON(t *testing.T) {
	b, err := harbor.DockerConfigJSON("cr.example.com:7443", "robot$p+forgeai", "s")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"auths":{"cr.example.com:7443":{"username":"robot$p+forgeai","password":"s","auth":"cm9ib3QkcCtmb3JnZWFpOnM="}}}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
}

func TestDeleteTag(t *testing.T) {
	ctx := context.Background()
	srv := harbortest.New("admin", "pw")
	defer srv.Close()
	c := harbor.New(srv.URL, "admin", "pw", true)
	if _, err := c.EnsureProject(ctx, "forge-demo"); err != nil {
		t.Fatal(err)
	}
	srv.AddArtifact("forge-demo", "team/train", "sha256:a", "v1", "latest")

	if err := c.DeleteTag(ctx, "forge-demo", "team/train", "sha256:a", "latest"); err != nil {
		t.Fatal(err)
	}
	arts, err := c.ListArtifacts(ctx, "forge-demo", "team/train")
	if err != nil || len(arts) != 1 || len(arts[0].TagNames()) != 1 || arts[0].TagNames()[0] != "v1" {
		t.Fatalf("after tag delete %+v %v", arts, err)
	}
	if err := c.DeleteTag(ctx, "forge-demo", "team/train", "sha256:a", "latest"); !errors.Is(err, harbor.ErrNotFound) {
		t.Fatalf("deleting a missing tag: %v", err)
	}
	// The last tag goes, and the image stays in the repository, untagged.
	if err := c.DeleteTag(ctx, "forge-demo", "team/train", "sha256:a", "v1"); err != nil {
		t.Fatal(err)
	}
	arts, err = c.ListArtifacts(ctx, "forge-demo", "team/train")
	if err != nil || len(arts) != 1 || len(arts[0].TagNames()) != 0 {
		t.Fatalf("after last tag delete %+v %v", arts, err)
	}
}
