package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/neuromaxer/appx/internal/project"
)

// handleListProjects returns the handler for GET /api/projects. It queries all
// projects via the Manager, runs the health checker to populate AppRunning on
// each project, and returns a JSON array ordered by creation date (newest first).
// Returns an empty array when no projects exist. This route is behind auth middleware.
func handleListProjects(pm *project.Manager, hc *project.HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projects, err := pm.Store.List()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		health := hc.Check(projects)
		for _, p := range projects {
			p.AppRunning = health[p.ID]
			p.ProjectDir = pm.ProjectDir(p.Name)
		}

		writeJSON(w, projects)
	}
}

// handleCreateProject returns the handler for POST /api/projects. It reads a
// JSON body with "name" (required slug), creates the project via the Manager
// (which auto-assigns a port from the 10000-10999 range and scaffolds the
// project directory), and returns 201 with the project JSON. Returns 400 for
// invalid name, 409 for duplicate names, and 507 when no ports are available.
func handleCreateProject(pm *project.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		proj, err := pm.Create(r.Context(), req.Name)
		if err != nil {
			if errors.Is(err, project.ErrInvalidName) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if errors.Is(err, project.ErrDuplicateName) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if errors.Is(err, project.ErrNoPortAvailable) {
				http.Error(w, err.Error(), http.StatusInsufficientStorage)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		writeJSON(w, proj)
	}
}

// handleGetProject returns the handler for GET /api/projects/{id}. It returns
// the project JSON with health status or 404 if not found. This route is behind
// auth middleware. The health checker probes the assigned port to populate AppRunning.
func handleGetProject(pm *project.Manager, hc *project.HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		proj, err := pm.Store.Get(id)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				http.Error(w, "project not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		proj.ProjectDir = pm.ProjectDir(proj.Name)
		health := hc.Check([]*project.Project{proj})
		proj.AppRunning = health[proj.ID]
		writeJSON(w, proj)
	}
}

// handleDeleteProject returns the handler for DELETE /api/projects/{id}. It
// removes the project's directory from disk and its record from the database.
// Returns 204 on success or 404 if not found.
func handleDeleteProject(pm *project.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := pm.Delete(r.Context(), id); err != nil {
			if errors.Is(err, project.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
