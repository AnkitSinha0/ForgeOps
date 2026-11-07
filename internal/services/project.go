package services

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/AnkitSinha0/forgeops/internal/models"
	"github.com/AnkitSinha0/forgeops/internal/repositories"
	"github.com/google/uuid"
)

// ProjectInput is what a caller may set on a project. TenantID and ID aren't in it, so
// no request can choose them.
type ProjectInput struct {
	Name    string
	RepoURL string
	Branch  string
}

const defaultBranch = "main"

// ProjectService manages projects.
type ProjectService struct {
	projects repositories.ProjectRepository
}

func NewProjectService(projects repositories.ProjectRepository) *ProjectService {
	return &ProjectService{projects: projects}
}

// Create adds a project. An empty branch means main.
func (s *ProjectService) Create(ctx context.Context, tenantID uuid.UUID, in ProjectInput) (*models.Project, error) {
	if in.Branch == "" {
		in.Branch = defaultBranch
	}
	if err := checkProject(in); err != nil {
		return nil, err
	}
	p := &models.Project{TenantID: tenantID, Name: in.Name, RepoURL: in.RepoURL, Branch: in.Branch}
	if err := s.projects.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *ProjectService) Get(ctx context.Context, tenantID, id uuid.UUID) (*models.Project, error) {
	return s.projects.FindByID(ctx, tenantID, id)
}

func (s *ProjectService) List(ctx context.Context, tenantID uuid.UUID, limit int, cursor string) (Page[models.Project], error) {
	if err := checkLimit(limit); err != nil {
		return Page[models.Project]{}, err
	}
	after, err := decodeNameCursor(cursor, slugPattern)
	if err != nil {
		return Page[models.Project]{}, err
	}
	rows, err := s.projects.List(ctx, tenantID, after, limit+1)
	if err != nil {
		return Page[models.Project]{}, err
	}
	return pageOf(rows, limit, func(p models.Project) string { return encodeCursor(p.Name) }), nil
}

// Update replaces all three fields. A full replace, not a partial one: merging a
// partial update means reading the row first, and two concurrent requests would then
// silently lose one of the changes.
func (s *ProjectService) Update(ctx context.Context, tenantID, id uuid.UUID, in ProjectInput) (*models.Project, error) {
	if err := checkProject(in); err != nil {
		return nil, err
	}
	p := &models.Project{ID: id, TenantID: tenantID, Name: in.Name, RepoURL: in.RepoURL, Branch: in.Branch}
	if err := s.projects.Update(ctx, p); err != nil {
		return nil, err
	}
	return s.projects.FindByID(ctx, tenantID, id)
}

func (s *ProjectService) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return s.projects.Delete(ctx, tenantID, id)
}

func checkProject(in ProjectInput) error {
	if !slugPattern.MatchString(in.Name) {
		return invalid("name", "must be 1-40 lowercase letters, digits or inner hyphens")
	}
	if err := checkRepoURL(in.RepoURL); err != nil {
		return err
	}
	return checkBranch(in.Branch)
}

// checkRepoURL accepts only https URLs with no embedded credentials. Phase 3 hands this
// value to a git client, so this is where file:// paths, ssh:// and tokens pasted into
// the URL get stopped. It can't stop a URL that resolves to an internal address,
// because that depends on DNS at clone time: Phase 3 has to check that when it clones.
func checkRepoURL(raw string) error {
	const message = "must be an https URL with no credentials, query or fragment"
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, " \t\r\n") {
		return invalid("repo_url", message)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalid("repo_url", message)
	}
	// url.Parse only checks that a port is digits, so 99999999 gets through it.
	if p := u.Port(); p != "" {
		if n, err := strconv.ParseUint(p, 10, 16); err != nil || n == 0 {
			return invalid("repo_url", "has an invalid port")
		}
	}
	return nil
}

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

// checkBranch is stricter than git's own rules on purpose. Not starting with "-" also
// means the value can never be read as a command-line option.
func checkBranch(branch string) error {
	if !branchPattern.MatchString(branch) || strings.Contains(branch, "..") || strings.Contains(branch, "//") ||
		strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, ".lock") {
		return invalid("branch", "is not a valid branch name")
	}
	return nil
}

// DeploymentService reads deployments. Creating them arrives with Phases 3-5.
type DeploymentService struct {
	projects    repositories.ProjectRepository
	deployments repositories.DeploymentRepository
}

func NewDeploymentService(projects repositories.ProjectRepository, deployments repositories.DeploymentRepository) *DeploymentService {
	return &DeploymentService{projects: projects, deployments: deployments}
}

// List returns a project's deployments, newest first. The project check makes another
// tenant's project a 404: without it the answer would be an empty list, which reads
// as "that project exists and has no deployments".
func (s *DeploymentService) List(ctx context.Context, tenantID, projectID uuid.UUID, limit int, cursor string) (Page[models.Deployment], error) {
	if err := checkLimit(limit); err != nil {
		return Page[models.Deployment]{}, err
	}
	after, err := decodeTimeCursor(cursor)
	if err != nil {
		return Page[models.Deployment]{}, err
	}
	if _, err := s.projects.FindByID(ctx, tenantID, projectID); err != nil {
		return Page[models.Deployment]{}, err
	}
	rows, err := s.deployments.ListByProject(ctx, tenantID, projectID, after, limit+1)
	if err != nil {
		return Page[models.Deployment]{}, err
	}
	return pageOf(rows, limit, func(d models.Deployment) string { return encodeTimeCursor(d.CreatedAt, d.ID) }), nil
}
