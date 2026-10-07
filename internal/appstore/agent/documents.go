package agent

import (
	"errors"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

type InstallRequest struct {
	Digest string `json:"digest,omitempty"`
}

type AppsDocument struct {
	Apps []AppDocument `json:"apps"`
}

type AppDocument struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Tagline     string                `json:"tagline"`
	Description string                `json:"description"`
	Category    string                `json:"category"`
	Version     string                `json:"version"`
	Author      string                `json:"author"`
	Website     string                `json:"website"`
	WebPort     uint16                `json:"webPort,omitempty"`
	Scheme      string                `json:"scheme"`
	Path        string                `json:"path"`
	State       appstore.InstallState `json:"state"`
	Running     int                   `json:"running"`
	Total       int                   `json:"total"`
	Job         *JobDocument          `json:"job,omitempty"`
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
	AppID      string         `json:"appId"`
	Title      string         `json:"title"`
	Version    string         `json:"version"`
	Project    string         `json:"project"`
	Images     []string       `json:"images"`
	Containers []string       `json:"containers"`
	Ports      []PortDocument `json:"ports"`
	Mounts     []MountJSON    `json:"mounts"`
	Digest     string         `json:"digest"`
	Compose    string         `json:"compose"`
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
			ID: app.ID, Title: app.Title, Tagline: app.Tagline, Description: app.Description, Category: app.Category,
			Version: app.Version, Author: app.Author, Website: app.Website, WebPort: app.WebPort, Scheme: app.Scheme, Path: app.Path,
			State: app.State, Running: app.Running, Total: app.Total,
		}
		if app.Job != nil {
			job := JobFromDomain(*app.Job)
			item.Job = &job
		}
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
		apps[i] = appstore.AppStatus{
			App: appstore.App{
				ID: item.ID, Title: item.Title, Tagline: item.Tagline, Description: item.Description, Category: item.Category,
				Version: item.Version, Author: item.Author, Website: item.Website, WebPort: item.WebPort, Scheme: item.Scheme, Path: item.Path,
			},
			State: item.State, Running: item.Running, Total: item.Total,
		}
		if item.Job != nil {
			job, err := item.Job.ToDomain()
			if err != nil {
				return nil, err
			}
			apps[i].Job = &job
		}
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
		Images: append([]string{}, plan.Images...), Containers: append([]string{}, plan.Containers...),
		Ports: make([]PortDocument, len(plan.Ports)), Mounts: make([]MountJSON, len(plan.Mounts)),
		Digest: plan.Digest, Compose: string(plan.Compose),
	}
	for i, port := range plan.Ports {
		document.Ports[i] = PortDocument{HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol, Purpose: port.Purpose}
	}
	for i, mount := range plan.Mounts {
		document.Mounts[i] = MountJSON{HostPath: mount.HostPath, ContainerPath: mount.ContainerPath, Kind: mount.Kind, ReadOnly: mount.ReadOnly, Purpose: mount.Purpose}
	}
	return document
}

func (document PlanDocument) ToDomain() appstore.Plan {
	plan := appstore.Plan{
		AppID: document.AppID, Title: document.Title, Version: document.Version, Project: document.Project,
		Images: document.Images, Containers: document.Containers, Digest: document.Digest, Compose: []byte(document.Compose),
	}
	for _, port := range document.Ports {
		plan.Ports = append(plan.Ports, appstore.PortMapping{HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol, Purpose: port.Purpose})
	}
	for _, mount := range document.Mounts {
		plan.Mounts = append(plan.Mounts, appstore.Mount{HostPath: mount.HostPath, ContainerPath: mount.ContainerPath, Kind: mount.Kind, ReadOnly: mount.ReadOnly, Purpose: mount.Purpose})
	}
	return plan
}
