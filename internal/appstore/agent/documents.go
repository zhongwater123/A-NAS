package agent

import (
	"errors"
	"net/netip"
	"slices"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

type InstallRequest struct {
	Digest string `json:"digest,omitempty"`
	// The reservation uses these only until the host account exists. Commit
	// checks the real account against that identity. The legacy install
	// endpoint ignores these fields and looks up the account itself (#176).
	UID int `json:"uid,omitempty"`
	GID int `json:"gid,omitempty"`
}

type ReservationDocument struct {
	Token string      `json:"token"`
	Job   JobDocument `json:"job"`
}

type PreparationRequest struct {
	Token string `json:"token"`
}

type AppsDocument struct {
	Apps []AppDocument `json:"apps"`
}

type AppDocument struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Tagline     string                `json:"tagline"`
	Description string                `json:"description"`
	Notes       string                `json:"notes"`
	Category    string                `json:"category"`
	Version     string                `json:"version"`
	Author      string                `json:"author"`
	Developer   string                `json:"developer"`
	Website     string                `json:"website"`
	Repo        string                `json:"repo"`
	Docs        string                `json:"docs"`
	Updated     string                `json:"updated"`
	WebPort     uint16                `json:"webPort,omitempty"`
	Scheme      string                `json:"scheme"`
	Path        string                `json:"path"`
	State       appstore.InstallState `json:"state"`
	Running     int                   `json:"running"`
	Total       int                   `json:"total"`
	Job         *JobDocument          `json:"job,omitempty"`

	// UpstreamNotes is empty from an agent that predates A-NAS notes.
	UpstreamNotes string `json:"upstreamNotes,omitempty"`
	// Problems lists the containers that are not running; an older agent
	// reports none.
	Problems []ProblemDocument `json:"problems,omitempty"`
	// Credentials are the logins generated for the app.
	Credentials []CredentialDocument `json:"credentials,omitempty"`
	// RuntimeOutdated marks an app installed under older runtime rules.
	RuntimeOutdated bool `json:"runtimeOutdated,omitempty"`
}

type CredentialDocument struct {
	Label    string `json:"label"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret"`
	Upstream bool   `json:"upstream,omitempty"`
}

func credentialsFromDomain(credentials []appstore.Credential) []CredentialDocument {
	var documents []CredentialDocument
	for _, credential := range credentials {
		documents = append(documents, CredentialDocument{Label: credential.Label, Username: credential.Username, Secret: credential.Secret, Upstream: credential.Upstream})
	}
	return documents
}

func credentialsToDomain(documents []CredentialDocument) []appstore.Credential {
	var credentials []appstore.Credential
	for _, document := range documents {
		credentials = append(credentials, appstore.Credential{Label: document.Label, Username: document.Username, Secret: document.Secret, Upstream: document.Upstream})
	}
	return credentials
}

type ProblemDocument struct {
	Service  string `json:"service"`
	State    string `json:"state"`
	ExitCode int    `json:"exitCode"`
}

type JobDocument struct {
	AppID      string             `json:"appId"`
	Action     appstore.JobAction `json:"action"`
	State      appstore.JobState  `json:"state"`
	StartedAt  string             `json:"startedAt"`
	FinishedAt string             `json:"finishedAt,omitempty"`
	Output     []string           `json:"output"`
	Error      string             `json:"error,omitempty"`
}

type PlanDocument struct {
	AppID    string       `json:"appId"`
	Title    string       `json:"title"`
	Version  string       `json:"version"`
	Project  string       `json:"project"`
	Identity IdentityJSON `json:"identity"`
	// Services is empty from an agent that predates runtime rules.
	Services   []ServiceDocument `json:"services,omitempty"`
	Images     []string          `json:"images"`
	Containers []string          `json:"containers"`
	Ports      []PortDocument    `json:"ports"`
	Mounts     []MountJSON       `json:"mounts"`
	Networks   []string          `json:"networks"`
	// AddressPools are CIDR prefixes; set whenever Networks is not empty.
	AddressPools []string `json:"addressPools"`
	Digest       string   `json:"digest"`
	Compose      string   `json:"compose"`

	// Credentials are the logins generated for the app.
	Credentials []CredentialDocument `json:"credentials,omitempty"`
}

type IdentityJSON struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
}

// ServiceDocument is how one service of a plan runs (appstore.ServiceRuntime).
type ServiceDocument struct {
	Service string                `json:"service"`
	RunAs   appstore.RunAs        `json:"runAs"`
	User    string                `json:"user,omitempty"`
	Groups  []string              `json:"groups,omitempty"`
	CapDrop []string              `json:"capDrop,omitempty"`
	Shared  appstore.SharedAccess `json:"shared,omitempty"`
}

type PortDocument struct {
	HostPort      uint16 `json:"hostPort"`
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol"`
	Purpose       string `json:"purpose,omitempty"`
}

type MountJSON struct {
	HostPath      string             `json:"hostPath"`
	ContainerPath string             `json:"containerPath"`
	Kind          appstore.MountKind `json:"kind"`
	ReadOnly      bool               `json:"readOnly"`
	Purpose       string             `json:"purpose,omitempty"`
}

func AppsFromDomain(apps []appstore.AppStatus) AppsDocument {
	document := AppsDocument{Apps: make([]AppDocument, len(apps))}
	for i, app := range apps {
		item := AppDocument{
			ID: app.ID, Title: app.Title, Tagline: app.Tagline, Description: app.Description, Notes: app.Notes, UpstreamNotes: app.UpstreamNotes,
			Category: app.Category, Version: app.Version, Author: app.Author, Developer: app.Developer, Website: app.Website, Repo: app.Repo,
			Docs: app.Docs, Updated: app.Updated, WebPort: app.WebPort, Scheme: app.Scheme, Path: app.Path,
			State: app.State, Running: app.Running, Total: app.Total,
		}
		if app.Job != nil {
			job := JobFromDomain(*app.Job)
			item.Job = &job
		}
		for _, problem := range app.Problems {
			item.Problems = append(item.Problems, ProblemDocument{Service: problem.Service, State: problem.State, ExitCode: problem.ExitCode})
		}
		item.Credentials = credentialsFromDomain(app.Credentials)
		item.RuntimeOutdated = app.RuntimeOutdated
		document.Apps[i] = item
	}
	return document
}

func (document AppsDocument) ToDomain() ([]appstore.AppStatus, error) {
	apps := make([]appstore.AppStatus, len(document.Apps))
	for i, item := range document.Apps {
		if !appstore.ValidID(item.ID) {
			return nil, errors.New("invalid app ID")
		}
		// An agent from before A-NAS categories reports the CasaOS ones.
		if !slices.Contains(appstore.Categories, item.Category) {
			item.Category = ""
		}
		apps[i] = appstore.AppStatus{
			ID: item.ID, Title: item.Title, Tagline: item.Tagline, Description: item.Description, Notes: item.Notes, UpstreamNotes: item.UpstreamNotes,
			Category: item.Category, Version: item.Version, Author: item.Author, Developer: item.Developer, Website: item.Website, Repo: item.Repo,
			Docs: item.Docs, Updated: item.Updated, WebPort: item.WebPort, Scheme: item.Scheme, Path: item.Path,
			State: item.State, Running: item.Running, Total: item.Total, RuntimeOutdated: item.RuntimeOutdated,
		}
		if item.Job != nil {
			job, err := item.Job.ToDomain()
			if err != nil {
				return nil, err
			}
			apps[i].Job = &job
		}
		for _, problem := range item.Problems {
			apps[i].Problems = append(apps[i].Problems, appstore.ContainerProblem{Service: problem.Service, State: problem.State, ExitCode: problem.ExitCode})
		}
		apps[i].Credentials = credentialsToDomain(item.Credentials)
	}
	return apps, nil
}

func JobFromDomain(job appstore.Job) JobDocument {
	document := JobDocument{
		AppID: job.AppID, Action: job.Action, State: job.State, StartedAt: job.StartedAt.UTC().Format(time.RFC3339),
		Output: append([]string{}, job.Output...), Error: job.Error,
	}
	if !job.FinishedAt.IsZero() {
		document.FinishedAt = job.FinishedAt.UTC().Format(time.RFC3339)
	}
	return document
}

func (document JobDocument) ToDomain() (appstore.Job, error) {
	started, err := time.Parse(time.RFC3339, document.StartedAt)
	if err != nil {
		return appstore.Job{}, errors.New("invalid job start time")
	}
	job := appstore.Job{AppID: document.AppID, Action: document.Action, State: document.State, StartedAt: started, Output: document.Output, Error: document.Error}
	if document.FinishedAt != "" {
		if job.FinishedAt, err = time.Parse(time.RFC3339, document.FinishedAt); err != nil {
			return appstore.Job{}, errors.New("invalid job finish time")
		}
	}
	return job, nil
}

func PlanFromDomain(plan appstore.Plan) PlanDocument {
	document := PlanDocument{
		AppID: plan.AppID, Title: plan.Title, Version: plan.Version, Project: plan.Project,
		Identity: IdentityJSON{Username: plan.Identity.Username, UID: plan.Identity.UID, GID: plan.Identity.GID},
		Images:   append([]string{}, plan.Images...), Containers: append([]string{}, plan.Containers...),
		Ports: make([]PortDocument, len(plan.Ports)), Mounts: make([]MountJSON, len(plan.Mounts)),
		Networks: append([]string{}, plan.Networks...), AddressPools: make([]string, len(plan.AddressPools)),
		Digest: plan.Digest, Compose: string(plan.Compose),
	}
	for i, pool := range plan.AddressPools {
		document.AddressPools[i] = pool.String()
	}
	for i, port := range plan.Ports {
		document.Ports[i] = PortDocument{HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol, Purpose: port.Purpose}
	}
	for i, mount := range plan.Mounts {
		document.Mounts[i] = MountJSON{HostPath: mount.HostPath, ContainerPath: mount.ContainerPath, Kind: mount.Kind, ReadOnly: mount.ReadOnly, Purpose: mount.Purpose}
	}
	for _, service := range plan.Services {
		document.Services = append(document.Services, ServiceDocument{
			Service: service.Service, RunAs: service.RunAs, User: service.User,
			Groups: append([]string(nil), service.Groups...), CapDrop: append([]string(nil), service.CapDrop...), Shared: service.Shared,
		})
	}
	document.Credentials = credentialsFromDomain(plan.Credentials)
	return document
}

func (document PlanDocument) ToDomain() appstore.Plan {
	plan := appstore.Plan{
		AppID: document.AppID, Title: document.Title, Version: document.Version, Project: document.Project,
		Identity: appstore.Identity{Username: document.Identity.Username, UID: document.Identity.UID, GID: document.Identity.GID},
		Images:   document.Images, Containers: document.Containers, Networks: document.Networks,
		Digest: document.Digest, Compose: []byte(document.Compose),
	}
	// Pools are shown to the owner, never acted on, so an unparsable one is dropped.
	for _, pool := range document.AddressPools {
		if prefix, err := netip.ParsePrefix(pool); err == nil {
			plan.AddressPools = append(plan.AddressPools, prefix)
		}
	}
	for _, port := range document.Ports {
		plan.Ports = append(plan.Ports, appstore.PortMapping{HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol, Purpose: port.Purpose})
	}
	for _, mount := range document.Mounts {
		plan.Mounts = append(plan.Mounts, appstore.Mount{HostPath: mount.HostPath, ContainerPath: mount.ContainerPath, Kind: mount.Kind, ReadOnly: mount.ReadOnly, Purpose: mount.Purpose})
	}
	for _, service := range document.Services {
		plan.Services = append(plan.Services, appstore.ServiceRuntime{
			Service: service.Service, RunAs: service.RunAs, User: service.User, Groups: service.Groups, CapDrop: service.CapDrop, Shared: service.Shared,
		})
	}
	plan.Credentials = credentialsToDomain(document.Credentials)
	return plan
}
