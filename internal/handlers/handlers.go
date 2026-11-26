// Package handlers is the control plane's HTTP layer. Handlers stay thin: decode the
// request, call one service, write the response. They never talk to the database, and
// every response goes through writeJSON or fail.
//
// Responses are built from the view types below, never from models. A model field
// someone adds later therefore can't reach a client unless it is added here on purpose,
// which matters for rows that hold ciphertext or key hashes.
package handlers

import (
	"net/http"
	"time"

	"github.com/AnkitSinha0/forgeops/internal/health"
	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/AnkitSinha0/forgeops/internal/services"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// Deps is everything the HTTP layer needs.
type Deps struct {
	Log         *zap.Logger
	Health      *health.Handler
	Tenants     *services.TenantService
	APIKeys     *services.APIKeyService
	Projects    *services.ProjectService
	EnvVars     *services.EnvVarService
	Deployments *services.DeploymentService
}

type handler struct {
	log         *zap.Logger
	tenants     *services.TenantService
	apiKeys     *services.APIKeyService
	projects    *services.ProjectService
	envVars     *services.EnvVarService
	deployments *services.DeploymentService
}

const perRequestTimeout = 10 * time.Second

// NewRouter builds the control plane API. Middleware runs top to bottom on the way in.
// Every route under /v1 is behind authenticate: the group has no way to add one
// outside it, and TestEveryV1RouteRequiresAuth walks the router to prove it.
func NewRouter(d Deps) chi.Router {
	h := &handler{
		log: d.Log, tenants: d.Tenants, apiKeys: d.APIKeys, projects: d.Projects,
		envVars: d.EnvVars, deployments: d.Deployments,
	}

	r := chi.NewRouter()
	r.Use(requestID, h.accessLog, h.recoverPanics, apiHeaders, requestTimeout(perRequestTimeout))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "not_found", "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})

	r.Get("/healthz", d.Health.Live)
	r.Get("/readyz", d.Health.Ready)

	r.Route("/v1", func(r chi.Router) {
		r.Use(h.authenticate)

		r.Get("/tenant", h.getTenant)

		r.Route("/api-keys", func(r chi.Router) {
			r.Post("/", h.createAPIKey)
			r.Get("/", h.listAPIKeys)
			r.Delete("/{id}", h.revokeAPIKey)
		})

		r.Route("/projects", func(r chi.Router) {
			r.Post("/", h.createProject)
			r.Get("/", h.listProjects)
			r.Get("/{id}", h.getProject)
			r.Put("/{id}", h.updateProject)
			r.Delete("/{id}", h.deleteProject)

			r.Get("/{id}/env", h.listEnvVars)
			r.Put("/{id}/env/{key}", h.setEnvVar)
			r.Delete("/{id}/env/{key}", h.deleteEnvVar)

			r.Get("/{id}/deployments", h.listDeployments)
		})
	})
	return r
}

// ── tenant ──────────────────────────────────────────────────────────────────

type tenantView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
}

func (h *handler) getTenant(w http.ResponseWriter, r *http.Request) {
	t, err := h.tenants.Get(r.Context(), tenantFrom(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tenantView{t.ID.String(), t.Name, t.Slug, t.CreatedAt.UTC()})
}

// ── API keys ────────────────────────────────────────────────────────────────

type apiKeyView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

func viewAPIKey(k models.APIKey) apiKeyView {
	v := apiKeyView{ID: k.ID.String(), Name: k.Name, Prefix: k.Prefix, CreatedAt: k.CreatedAt.UTC()}
	if k.RevokedAt != nil {
		at := k.RevokedAt.UTC()
		v.RevokedAt = &at
	}
	return v
}

func (h *handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	k, plaintext, err := h.apiKeys.Create(r.Context(), tenantFrom(r.Context()), req.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The only response that ever contains the key.
	writeJSON(w, http.StatusCreated, struct {
		apiKeyView
		Key string `json:"key"`
	}{viewAPIKey(*k), plaintext})
}

func (h *handler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := pageParams(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := h.apiKeys.List(r.Context(), tenantFrom(r.Context()), limit, cursor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listOf(page, viewAPIKey))
}

func (h *handler) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err == nil {
		err = h.apiKeys.Revoke(r.Context(), tenantFrom(r.Context()), id)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── projects ────────────────────────────────────────────────────────────────

type projectView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RepoURL   string    `json:"repo_url"`
	Branch    string    `json:"branch"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func viewProject(p models.Project) projectView {
	return projectView{p.ID.String(), p.Name, p.RepoURL, p.Branch, p.CreatedAt.UTC(), p.UpdatedAt.UTC()}
}

// projectRequest deliberately has no tenant_id or id field. Together with
// DisallowUnknownFields, sending either is a 400.
type projectRequest struct {
	Name    string `json:"name"`
	RepoURL string `json:"repo_url"`
	Branch  string `json:"branch"`
}

func (p projectRequest) input() services.ProjectInput {
	return services.ProjectInput{Name: p.Name, RepoURL: p.RepoURL, Branch: p.Branch}
}

func (h *handler) createProject(w http.ResponseWriter, r *http.Request) {
	var req projectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := h.projects.Create(r.Context(), tenantFrom(r.Context()), req.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/projects/"+p.ID.String())
	writeJSON(w, http.StatusCreated, viewProject(*p))
}

func (h *handler) listProjects(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := pageParams(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := h.projects.List(r.Context(), tenantFrom(r.Context()), limit, cursor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listOf(page, viewProject))
}

func (h *handler) getProject(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := h.projects.Get(r.Context(), tenantFrom(r.Context()), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewProject(*p))
}

func (h *handler) updateProject(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req projectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := h.projects.Update(r.Context(), tenantFrom(r.Context()), id, req.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewProject(*p))
}

func (h *handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err == nil {
		err = h.projects.Delete(r.Context(), tenantFrom(r.Context()), id)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── env vars ────────────────────────────────────────────────────────────────

// envVarView has no value field: values are write-only through the API.
type envVarView struct {
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func viewEnvVar(e models.EnvVar) envVarView {
	return envVarView{e.Key, e.CreatedAt.UTC(), e.UpdatedAt.UTC()}
}

func (h *handler) setEnvVar(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A pointer, so a missing value is an error while an empty string is a real value.
	var req struct {
		Value *string `json:"value"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Value == nil {
		h.fail(w, r, &services.ValidationError{Field: "value", Message: "is required"})
		return
	}
	e, err := h.envVars.Set(r.Context(), tenantFrom(r.Context()), projectID, envKeyParam(r), *req.Value)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewEnvVar(*e))
}

func (h *handler) listEnvVars(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := h.envVars.List(r.Context(), tenantFrom(r.Context()), projectID, limit, cursor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listOf(page, viewEnvVar))
}

func (h *handler) deleteEnvVar(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseID(chi.URLParam(r, "id"))
	if err == nil {
		err = h.envVars.Delete(r.Context(), tenantFrom(r.Context()), projectID, envKeyParam(r))
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── deployments ─────────────────────────────────────────────────────────────

type deploymentView struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	BuildID   string    `json:"build_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func viewDeployment(d models.Deployment) deploymentView {
	return deploymentView{d.ID.String(), d.ProjectID.String(), d.BuildID.String(), string(d.Status), d.CreatedAt.UTC(), d.UpdatedAt.UTC()}
}

func (h *handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := h.deployments.List(r.Context(), tenantFrom(r.Context()), projectID, limit, cursor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listOf(page, viewDeployment))
}
