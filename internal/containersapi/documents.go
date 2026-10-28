package containersapi

import (
	"time"

	"github.com/zhongwater123/A-NAS/internal/containers"
)

// The container API's public documents (api/components/containers.yaml).
// The container agent speaks its own (internal/containers/agent).

type snapshot struct {
	DataSource DataSource  `json:"dataSource"`
	ObservedAt string      `json:"observedAt"`
	Engine     engine      `json:"engine"`
	Containers []container `json:"containers"`
	Images     []image     `json:"images"`
}

type engine struct {
	Version    string `json:"version"`
	APIVersion string `json:"apiVersion"`
}

type container struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Image     string           `json:"image"`
	ImageID   string           `json:"imageId,omitempty"`
	State     containers.State `json:"state"`
	Status    string           `json:"status"`
	CreatedAt string           `json:"createdAt"`
	Ports     []port           `json:"ports"`
	Project   string           `json:"project,omitempty"`
	Usage     *usage           `json:"usage,omitempty"`
}

type port struct {
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      uint16 `json:"hostPort,omitempty"`
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type usage struct {
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryBytes      uint64  `json:"memoryBytes"`
	MemoryLimitBytes uint64  `json:"memoryLimitBytes"`
}

type image struct {
	ID        string   `json:"id"`
	Tags      []string `json:"tags"`
	SizeBytes int64    `json:"sizeBytes"`
	CreatedAt string   `json:"createdAt"`
}

type logs struct {
	Lines []logLine `json:"lines"`
}

type logLine struct {
	Stream string `json:"stream"`
	Time   string `json:"time,omitempty"`
	Text   string `json:"text"`
}

func snapshotDocument(dataSource DataSource, from containers.Snapshot) snapshot {
	document := snapshot{
		DataSource: dataSource, ObservedAt: from.ObservedAt.UTC().Format(time.RFC3339),
		Engine:     engine{Version: from.Engine.Version, APIVersion: from.Engine.APIVersion},
		Containers: make([]container, len(from.Containers)), Images: make([]image, len(from.Images)),
	}
	for i, item := range from.Containers {
		ports := make([]port, len(item.Ports))
		for j, p := range item.Ports {
			ports[j] = port{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Protocol: p.Protocol}
		}
		document.Containers[i] = container{
			ID: item.ID, Name: item.Name, Image: item.Image, ImageID: item.ImageID, State: item.State, Status: item.Status,
			CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339), Ports: ports, Project: item.Project,
		}
		if item.Usage != nil {
			document.Containers[i].Usage = &usage{CPUPercent: item.Usage.CPUPercent, MemoryBytes: item.Usage.MemoryBytes, MemoryLimitBytes: item.Usage.MemoryLimitBytes}
		}
	}
	for i, item := range from.Images {
		tags := item.Tags
		if tags == nil {
			tags = []string{}
		}
		document.Images[i] = image{ID: item.ID, Tags: tags, SizeBytes: item.SizeBytes, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339)}
	}
	return document
}

func logsDocument(lines []containers.LogLine) logs {
	document := logs{Lines: make([]logLine, len(lines))}
	for i, line := range lines {
		document.Lines[i] = logLine{Stream: line.Stream, Text: line.Text}
		if !line.Time.IsZero() {
			document.Lines[i].Time = line.Time.UTC().Format(time.RFC3339Nano)
		}
	}
	return document
}
