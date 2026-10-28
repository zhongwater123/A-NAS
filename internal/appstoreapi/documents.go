package appstoreapi

import (
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// The App Center's public documents (api/components/apps.yaml). The
// container agent speaks its own (internal/appstore/agent), so either can
// change without the other.

type installRequest struct {
	Digest string `json:"digest"`
}

type appsResponse struct {
	DataSource DataSource `json:"dataSource"`
	Apps       []app      `json:"apps"`
}

type app struct {
	ID              string                `json:"id"`
	Title           string                `json:"title"`
	Tagline         string                `json:"tagline"`
	Description     string                `json:"description"`
	Notes           string                `json:"notes"`
	Category        string                `json:"category"`
	Version         string                `json:"version"`
	Author          string                `json:"author"`
	Developer       string                `json:"developer"`
	Website         string                `json:"website"`
	Repo            string                `json:"repo"`
	Docs            string                `json:"docs"`
	Updated         string                `json:"updated"`
	WebPort         uint16                `json:"webPort,omitempty"`
	Scheme          string                `json:"scheme"`
	Path            string                `json:"path"`
	State           appstore.InstallState `json:"state"`
	Running         int                   `json:"running"`
	Total           int                   `json:"total"`
	Job             *job                  `json:"job,omitempty"`
	UpstreamNotes   string                `json:"upstreamNotes,omitempty"`
	Problems        []problem             `json:"problems,omitempty"`
	Credentials     []credential          `json:"credentials,omitempty"`
	RuntimeOutdated bool                  `json:"runtimeOutdated,omitempty"`
}

type credential struct {
	Label    string `json:"label"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret"`
	Upstream bool   `json:"upstream,omitempty"`
}

type problem struct {
	Service  string `json:"service"`
	State    string `json:"state"`
	ExitCode int    `json:"exitCode"`
}

type job struct {
	AppID      string             `json:"appId"`
	Action     appstore.JobAction `json:"action"`
	State      appstore.JobState  `json:"state"`
	StartedAt  string             `json:"startedAt"`
	FinishedAt string             `json:"finishedAt,omitempty"`
	Output     []string           `json:"output"`
	Error      string             `json:"error,omitempty"`
}

type plan struct {
	AppID        string       `json:"appId"`
	Title        string       `json:"title"`
	Version      string       `json:"version"`
	Project      string       `json:"project"`
	Identity     identity     `json:"identity"`
	Services     []service    `json:"services,omitempty"`
	Images       []string     `json:"images"`
	Containers   []string     `json:"containers"`
	Ports        []port       `json:"ports"`
	Mounts       []mount      `json:"mounts"`
	Networks     []string     `json:"networks"`
	AddressPools []string     `json:"addressPools"`
	Digest       string       `json:"digest"`
	Compose      string       `json:"compose"`
	Credentials  []credential `json:"credentials,omitempty"`
}

type identity struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
}

type service struct {
	Service string                `json:"service"`
	RunAs   appstore.RunAs        `json:"runAs"`
	User    string                `json:"user,omitempty"`
	Groups  []string              `json:"groups,omitempty"`
	CapDrop []string              `json:"capDrop,omitempty"`
	Shared  appstore.SharedAccess `json:"shared,omitempty"`
}

type port struct {
	HostPort      uint16 `json:"hostPort"`
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol"`
	Purpose       string `json:"purpose,omitempty"`
}

type mount struct {
	HostPath      string             `json:"hostPath"`
	ContainerPath string             `json:"containerPath"`
	Kind          appstore.MountKind `json:"kind"`
	ReadOnly      bool               `json:"readOnly"`
	Purpose       string             `json:"purpose,omitempty"`
}

func appsDocument(apps []appstore.AppStatus) []app {
	documents := make([]app, len(apps))
	for i, status := range apps {
		document := app{
			ID: status.ID, Title: status.Title, Tagline: status.Tagline, Description: status.Description, Notes: status.Notes,
			UpstreamNotes: status.UpstreamNotes, Category: status.Category, Version: status.Version, Author: status.Author,
			Developer: status.Developer, Website: status.Website, Repo: status.Repo, Docs: status.Docs, Updated: status.Updated,
			WebPort: status.WebPort, Scheme: status.Scheme, Path: status.Path, State: status.State, Running: status.Running,
			Total: status.Total, Credentials: credentials(status.Credentials), RuntimeOutdated: status.RuntimeOutdated,
		}
		if status.Job != nil {
			job := jobDocument(*status.Job)
			document.Job = &job
		}
		for _, p := range status.Problems {
			document.Problems = append(document.Problems, problem{Service: p.Service, State: p.State, ExitCode: p.ExitCode})
		}
		documents[i] = document
	}
	return documents
}

func credentials(items []appstore.Credential) []credential {
	var documents []credential
	for _, item := range items {
		documents = append(documents, credential{Label: item.Label, Username: item.Username, Secret: item.Secret, Upstream: item.Upstream})
	}
	return documents
}

func jobDocument(from appstore.Job) job {
	document := job{
		AppID: from.AppID, Action: from.Action, State: from.State, StartedAt: from.StartedAt.UTC().Format(time.RFC3339),
		Output: append([]string{}, from.Output...), Error: from.Error,
	}
	if !from.FinishedAt.IsZero() {
		document.FinishedAt = from.FinishedAt.UTC().Format(time.RFC3339)
	}
	return document
}

func planDocument(from appstore.Plan) plan {
	document := plan{
		AppID: from.AppID, Title: from.Title, Version: from.Version, Project: from.Project,
		Identity: identity{Username: from.Identity.Username, UID: from.Identity.UID, GID: from.Identity.GID},
		Images:   append([]string{}, from.Images...), Containers: append([]string{}, from.Containers...),
		Ports: make([]port, len(from.Ports)), Mounts: make([]mount, len(from.Mounts)),
		Networks: append([]string{}, from.Networks...), AddressPools: make([]string, len(from.AddressPools)),
		Digest: from.Digest, Compose: string(from.Compose), Credentials: credentials(from.Credentials),
	}
	for i, pool := range from.AddressPools {
		document.AddressPools[i] = pool.String()
	}
	for i, p := range from.Ports {
		document.Ports[i] = port{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol, Purpose: p.Purpose}
	}
	for i, m := range from.Mounts {
		document.Mounts[i] = mount{HostPath: m.HostPath, ContainerPath: m.ContainerPath, Kind: m.Kind, ReadOnly: m.ReadOnly, Purpose: m.Purpose}
	}
	for _, s := range from.Services {
		document.Services = append(document.Services, service{
			Service: s.Service, RunAs: s.RunAs, User: s.User,
			Groups: append([]string(nil), s.Groups...), CapDrop: append([]string(nil), s.CapDrop...), Shared: s.Shared,
		})
	}
	return document
}
