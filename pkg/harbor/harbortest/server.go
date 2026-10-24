// Package harbortest is an in-memory Harbor API for tests.
package harbortest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Server is a fake Harbor holding projects, robots, quotas and repositories.
type Server struct {
	*httptest.Server
	Username, Password string

	mu       sync.Mutex
	nextID   int64
	Projects map[string]int64            // name -> id
	Quotas   map[int64]int64             // project id -> storage bytes
	Robots   map[int64]Robot             // id -> robot
	Repos    map[string]map[string][]Art // project -> repo -> artifacts
}

type Robot struct {
	ID        int64
	Name      string
	ProjectID int64
	Secret    string
}

type Art struct {
	Digest string
	Tags   []string
}

// New starts a fake Harbor with the given admin credentials.
func New(username, password string) *Server {
	s := &Server{
		Username: username, Password: password,
		Projects: map[string]int64{}, Quotas: map[int64]int64{}, Robots: map[int64]Robot{},
		Repos: map[string]map[string][]Art{},
	}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	return s
}

func (s *Server) id() int64 { s.nextID++; return s.nextID }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	u, p, ok := r.BasicAuth()
	if !ok {
		return false
	}
	if u == s.Username && p == s.Password {
		return true
	}
	for _, rb := range s.Robots {
		if rb.Name == u && rb.Secret == p {
			return true
		}
	}
	return false
}

// RobotSecret returns the current secret of a robot by login name.
func (s *Server) RobotSecret(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rb := range s.Robots {
		if rb.Name == name {
			return rb.Secret
		}
	}
	return ""
}

// RemoveRobot deletes a robot by login name, as if it was removed in Harbor.
func (s *Server) RemoveRobot(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rb := range s.Robots {
		if rb.Name == name {
			delete(s.Robots, id)
		}
	}
}

// HasProject reports whether a project exists.
func (s *Server) HasProject(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Projects[name]
	return ok
}

// AddArtifact adds an image to a project.
func (s *Server) AddArtifact(project, repo, digest string, tags ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Repos[project] == nil {
		s.Repos[project] = map[string][]Art{}
	}
	s.Repos[project][repo] = append(s.Repos[project][repo], Art{Digest: digest, Tags: tags})
}

func page[T any](r *http.Request, items []T) []T {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p > 1 {
		return []T{}
	}
	return items
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v2.0")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := range parts {
		parts[i], _ = url.PathUnescape(parts[i])
	}
	switch {
	case r.Method == http.MethodPost && path == "/projects":
		var in struct {
			Name string `json:"project_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if _, ok := s.Projects[in.Name]; ok {
			writeJSON(w, http.StatusConflict, nil)
			return
		}
		id := s.id()
		s.Projects[in.Name] = id
		s.Quotas[id] = -1
		writeJSON(w, http.StatusCreated, nil)
	case parts[0] == "projects" && len(parts) == 2:
		id, ok := s.Projects[parts[1]]
		if !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		if r.Method == http.MethodDelete {
			if len(s.Repos[parts[1]]) > 0 {
				writeJSON(w, http.StatusPreconditionFailed, nil)
				return
			}
			delete(s.Projects, parts[1])
			for rid, rb := range s.Robots {
				if rb.ProjectID == id {
					delete(s.Robots, rid)
				}
			}
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project_id": id, "name": parts[1], "repo_count": len(s.Repos[parts[1]])})
	case parts[0] == "projects" && len(parts) == 3 && parts[2] == "repositories":
		if _, ok := s.Projects[parts[1]]; !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		out := []map[string]any{}
		for name, arts := range s.Repos[parts[1]] {
			out = append(out, map[string]any{"name": parts[1] + "/" + name, "artifact_count": len(arts)})
		}
		writeJSON(w, http.StatusOK, page(r, out))
	case parts[0] == "projects" && len(parts) == 4 && parts[2] == "repositories":
		repo, _ := url.PathUnescape(parts[3])
		if _, ok := s.Repos[parts[1]][repo]; !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		delete(s.Repos[parts[1]], repo)
		writeJSON(w, http.StatusOK, nil)
	case parts[0] == "projects" && len(parts) >= 5 && parts[4] == "artifacts":
		repo, _ := url.PathUnescape(parts[3])
		arts, ok := s.Repos[parts[1]][repo]
		if !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		if r.Method == http.MethodDelete && len(parts) == 8 && parts[6] == "tags" {
			for i := range arts {
				if arts[i].Digest != parts[5] {
					continue
				}
				kept := []string{}
				for _, t := range arts[i].Tags {
					if t != parts[7] {
						kept = append(kept, t)
					}
				}
				if len(kept) == len(arts[i].Tags) {
					break
				}
				arts[i].Tags = kept
				writeJSON(w, http.StatusOK, nil)
				return
			}
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		if r.Method == http.MethodDelete && len(parts) == 6 {
			kept := arts[:0]
			for _, a := range arts {
				if a.Digest != parts[5] {
					kept = append(kept, a)
				}
			}
			s.Repos[parts[1]][repo] = kept
			writeJSON(w, http.StatusOK, nil)
			return
		}
		out := []map[string]any{}
		for _, a := range arts {
			tags := []map[string]string{}
			for _, t := range a.Tags {
				tags = append(tags, map[string]string{"name": t})
			}
			out = append(out, map[string]any{"digest": a.Digest, "size": 1024, "tags": tags})
		}
		writeJSON(w, http.StatusOK, page(r, out))
	case path == "/quotas":
		pid, _ := strconv.ParseInt(r.URL.Query().Get("reference_id"), 10, 64)
		writeJSON(w, http.StatusOK, []map[string]any{{"id": pid, "hard": map[string]int64{"storage": s.Quotas[pid]}}})
	case parts[0] == "quotas" && len(parts) == 2 && r.Method == http.MethodPut:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		var in struct {
			Hard map[string]int64 `json:"hard"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.Quotas[id] = in.Hard["storage"]
		writeJSON(w, http.StatusOK, nil)
	case path == "/robots" && r.Method == http.MethodGet:
		var pid int64
		_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Query().Get("q"), "Level=project,"), "ProjectID=%d", &pid)
		out := []map[string]any{}
		for _, rb := range s.Robots {
			if rb.ProjectID == pid {
				out = append(out, map[string]any{"id": rb.ID, "name": rb.Name})
			}
		}
		writeJSON(w, http.StatusOK, page(r, out))
	case path == "/robots" && r.Method == http.MethodPost:
		var in struct {
			Name        string `json:"name"`
			Permissions []struct {
				Namespace string `json:"namespace"`
			} `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		project := in.Permissions[0].Namespace
		rb := Robot{ID: s.id(), Name: "robot$" + project + "+" + in.Name, ProjectID: s.Projects[project], Secret: "initial"}
		s.Robots[rb.ID] = rb
		writeJSON(w, http.StatusCreated, map[string]any{"id": rb.ID, "name": rb.Name, "secret": rb.Secret})
	case parts[0] == "robots" && len(parts) == 2 && r.Method == http.MethodPatch:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		rb, ok := s.Robots[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		var in struct {
			Secret string `json:"secret"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		rb.Secret = in.Secret
		s.Robots[id] = rb
		writeJSON(w, http.StatusOK, nil)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no route " + r.Method + " " + path})
	}
}
