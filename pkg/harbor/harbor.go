// Package harbor is a small Harbor v2 API client. The operator uses admin
// credentials to manage one Harbor project and robot account per forgeai
// project; the GUI uses the robot account to list and delete images.
package harbor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is returned for HTTP 404.
var ErrNotFound = errors.New("not found")

// RobotName is the short name of the push robot of every project.
const RobotName = "forgeai"

// Client calls the Harbor API with basic auth.
type Client struct {
	// BaseURL is the Harbor URL, e.g. https://cr.example.com:7443.
	BaseURL  string
	Username string
	Password string
	HTTP     *http.Client
}

// New returns a client. insecure skips TLS verification (private CA).
func New(baseURL, username, password string, insecure bool) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // private CA, opt-in
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), Username: username, Password: password,
		HTTP: &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

// StatusError is a non-2xx Harbor response.
type StatusError struct {
	Method, Path string
	Code         int
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("harbor %s %s: HTTP %d: %s", e.Method, e.Path, e.Code, e.Body)
}

func (e *StatusError) Is(target error) bool {
	return target == ErrNotFound && e.Code == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := c.BaseURL + "/api/v2.0" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &StatusError{Method: method, Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("harbor %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

// getAll pages through a list endpoint.
func getAll[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		q := url.Values{}
		maps.Copy(q, query)
		q.Set("page", fmt.Sprint(page))
		q.Set("page_size", "100")
		var items []T
		if err := c.do(ctx, http.MethodGet, path, q, nil, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < 100 {
			return all, nil
		}
	}
}

func projectPath(project string) string { return "/projects/" + url.PathEscape(project) }

// repoPath escapes a repository name inside a project; Harbor wants "/" in
// the repository part double-escaped.
func repoPath(project, repo string) string {
	return projectPath(project) + "/repositories/" + url.PathEscape(strings.ReplaceAll(repo, "/", "%2F"))
}

// Project is a Harbor project.
type Project struct {
	ID        int64  `json:"project_id"`
	Name      string `json:"name"`
	RepoCount int64  `json:"repo_count"`
}

// GetProject returns a project by name.
func (c *Client) GetProject(ctx context.Context, name string) (*Project, error) {
	p := &Project{}
	if err := c.do(ctx, http.MethodGet, projectPath(name), nil, nil, p); err != nil {
		return nil, err
	}
	return p, nil
}

// EnsureProject creates a private project if it does not exist.
func (c *Client) EnsureProject(ctx context.Context, name string) (*Project, error) {
	p, err := c.GetProject(ctx, name)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	in := map[string]any{"project_name": name, "metadata": map[string]string{"public": "false"}, "storage_limit": -1}
	if err := c.do(ctx, http.MethodPost, "/projects", nil, in, nil); err != nil {
		var se *StatusError
		if !errors.As(err, &se) || se.Code != http.StatusConflict {
			return nil, err
		}
	}
	return c.GetProject(ctx, name)
}

// SetStorageQuota sets the project storage quota in bytes (-1 is unlimited).
func (c *Client) SetStorageQuota(ctx context.Context, p *Project, limit int64) error {
	type quota struct {
		ID   int64            `json:"id"`
		Hard map[string]int64 `json:"hard"`
	}
	var qs []quota
	q := url.Values{"reference": {"project"}, "reference_id": {fmt.Sprint(p.ID)}} //nolint:goconst // Harbor query keys
	if err := c.do(ctx, http.MethodGet, "/quotas", q, nil, &qs); err != nil {
		return err
	}
	if len(qs) == 0 {
		return fmt.Errorf("harbor: no quota for project %s", p.Name)
	}
	if qs[0].Hard["storage"] == limit {
		return nil
	}
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/quotas/%d", qs[0].ID), nil,
		map[string]any{"hard": map[string]int64{"storage": limit}}, nil)
	return err
}

// DeleteProject deletes all repositories and the project. A missing project is not an error.
func (c *Client) DeleteProject(ctx context.Context, name string) error {
	repos, err := c.ListRepositories(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, r := range repos {
		if err := c.DeleteRepository(ctx, name, r.Name); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if err := c.do(ctx, http.MethodDelete, projectPath(name), nil, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// Robot is a robot account.
type Robot struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RobotAccount is the full login name of a project robot.
func RobotAccount(prefix, project, name string) string { return prefix + project + "+" + name }

// robotAccess lets the robot push, pull, list and delete images.
//
//nolint:goconst // Harbor resource and action names are a fixed vocabulary.
var robotAccess = [][2]string{
	{"repository", "pull"}, {"repository", "push"}, {"repository", "list"}, {"repository", "read"},
	{"repository", "delete"}, {"artifact", "list"}, {"artifact", "read"}, {"artifact", "delete"},
	{"tag", "list"}, {"tag", "create"}, {"tag", "delete"},
}

// robotPermissions lets the robot push, pull, list and delete images.
func robotPermissions(project string) []map[string]any {
	access := make([]map[string]string, 0, len(robotAccess))
	for _, ra := range robotAccess {
		access = append(access, map[string]string{"resource": ra[0], "action": ra[1]})
	}
	return []map[string]any{{"kind": "project", "namespace": project, "access": access}}
}

func (c *Client) findRobot(ctx context.Context, p *Project, name string) (*Robot, error) {
	robots, err := getAll[Robot](ctx, c, "/robots", url.Values{"q": {fmt.Sprintf("Level=project,ProjectID=%d", p.ID)}})
	if err != nil {
		return nil, err
	}
	for i := range robots {
		if strings.HasSuffix(robots[i].Name, p.Name+"+"+name) {
			return &robots[i], nil
		}
	}
	return nil, ErrNotFound
}

// HasRobot reports whether the project robot exists in Harbor.
func (c *Client) HasRobot(ctx context.Context, p *Project, name string) (bool, error) {
	_, err := c.findRobot(ctx, p, name)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// EnsureRobot creates (or resets) the project robot with the given secret and
// returns its login name. Harbor only reveals robot secrets on creation, so
// the caller generates the secret and stores it.
func (c *Client) EnsureRobot(ctx context.Context, p *Project, name, secret string) (string, error) {
	r, err := c.findRobot(ctx, p, name)
	if errors.Is(err, ErrNotFound) {
		in := map[string]any{
			"name": name, "level": "project", "duration": -1, "disable": false,
			"description": "forgeai push/pull robot", "permissions": robotPermissions(p.Name),
		}
		var out struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := c.do(ctx, http.MethodPost, "/robots", nil, in, &out); err != nil {
			return "", err
		}
		r = &Robot{ID: out.ID, Name: out.Name}
	} else if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/robots/%d", r.ID)
	if err := c.do(ctx, http.MethodPatch, path, nil, map[string]string{"secret": secret}, nil); err != nil {
		return "", err
	}
	return r.Name, nil
}

// GenerateSecret returns a random secret satisfying Harbor's robot secret
// rules (8-128 characters with upper and lower case letters and digits).
func GenerateSecret() (string, error) {
	const lower, upper, digit = "abcdefghijkmnopqrstuvwxyz", "ABCDEFGHJKLMNPQRSTUVWXYZ", "23456789"
	all := lower + upper + digit
	pick := func(set string) (byte, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return 0, err
		}
		return set[n.Int64()], nil
	}
	b := make([]byte, 0, 32)
	for _, set := range []string{lower, upper, digit} {
		ch, err := pick(set)
		if err != nil {
			return "", err
		}
		b = append(b, ch)
	}
	for len(b) < 32 {
		ch, err := pick(all)
		if err != nil {
			return "", err
		}
		b = append(b, ch)
	}
	return string(b), nil
}

// Repository is an image repository.
type Repository struct {
	// Name is the repository without the project prefix.
	Name          string `json:"name"`
	ArtifactCount int64  `json:"artifact_count"`
	PullCount     int64  `json:"pull_count"`
	UpdateTime    string `json:"update_time"`
}

// ListRepositories returns the repositories of a project.
func (c *Client) ListRepositories(ctx context.Context, project string) ([]Repository, error) {
	repos, err := getAll[Repository](ctx, c, projectPath(project)+"/repositories", nil)
	if err != nil {
		return nil, err
	}
	for i := range repos {
		repos[i].Name = strings.TrimPrefix(repos[i].Name, project+"/")
	}
	return repos, nil
}

// DeleteRepository deletes a repository with all its artifacts.
func (c *Client) DeleteRepository(ctx context.Context, project, repo string) error {
	err := c.do(ctx, http.MethodDelete, repoPath(project, repo), nil, nil, nil)
	return err
}

// Artifact is one image manifest with its tags.
type Artifact struct {
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
	PushTime string `json:"push_time"`
	PullTime string `json:"pull_time"`
	Tags     []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

// TagNames returns the tag names of an artifact.
func (a Artifact) TagNames() []string {
	out := make([]string, 0, len(a.Tags))
	for _, t := range a.Tags {
		out = append(out, t.Name)
	}
	return out
}

// ListArtifacts returns the artifacts of a repository with their tags.
func (c *Client) ListArtifacts(ctx context.Context, project, repo string) ([]Artifact, error) {
	return getAll[Artifact](ctx, c, repoPath(project, repo)+"/artifacts", url.Values{"with_tag": {"true"}})
}

// DeleteArtifact deletes an artifact (all its tags) by digest or tag.
func (c *Client) DeleteArtifact(ctx context.Context, project, repo, reference string) error {
	err := c.do(ctx, http.MethodDelete, repoPath(project, repo)+"/artifacts/"+url.PathEscape(reference), nil, nil, nil)
	return err
}

// DeleteTag removes one tag from an artifact. The artifact keeps its digest and
// its other tags; without any tag left it stays in the repository, untagged.
func (c *Client) DeleteTag(ctx context.Context, project, repo, reference, tag string) error {
	return c.do(ctx, http.MethodDelete,
		repoPath(project, repo)+"/artifacts/"+url.PathEscape(reference)+"/tags/"+url.PathEscape(tag), nil, nil, nil)
}

// DockerConfigJSON renders a .dockerconfigjson for one registry.
func DockerConfigJSON(registry, username, password string) ([]byte, error) {
	type entry struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Auth     string `json:"auth"`
	}
	auth := basicAuth(username, password)
	return json.Marshal(map[string]map[string]entry{
		"auths": {registry: {Username: username, Password: password, Auth: auth}},
	})
}
