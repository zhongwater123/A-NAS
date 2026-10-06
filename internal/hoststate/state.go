// Package hoststate defines the read-only state observed from an A-NAS host.
package hoststate

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrInvalidResourceID = errors.New("invalid stable resource ID")

// ResourceID is an opaque, path-independent identity for a host resource.
type ResourceID struct {
	value string
}

func NewResourceID(value string) (ResourceID, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, `/\\`) {
		return ResourceID{}, ErrInvalidResourceID
	}
	return ResourceID{value: value}, nil
}

func (id ResourceID) String() string {
	return id.value
}

type Health string

const (
	HealthHealthy  Health = "healthy"
	HealthWarning  Health = "warning"
	HealthCritical Health = "critical"
	HealthUnknown  Health = "unknown"
)

type DiskRole string

const (
	DiskRoleSystem     DiskRole = "system"
	DiskRoleData       DiskRole = "data"
	DiskRoleUnassigned DiskRole = "unassigned"
)

type Transport string

const (
	TransportNVMe    Transport = "nvme"
	TransportSATA    Transport = "sata"
	TransportUSB     Transport = "usb"
	TransportUnknown Transport = "unknown"
)

type OperatingSystem struct {
	Name    string
	Version string
}

type System struct {
	ID              ResourceID
	Hostname        string
	OperatingSystem OperatingSystem
	Architecture    string
	UptimeSeconds   uint64
	Health          Health
}

type Disk struct {
	ID                 ResourceID
	Model              string
	Transport          Transport
	CapacityBytes      uint64
	Rotational         bool
	Role               DiskRole
	Health             Health
	TemperatureCelsius *int
}

type State struct {
	ObservedAt time.Time
	System     System
	Disks      []Disk
}

// Reader returns one point-in-time observation of the host and its disks.
type Reader interface {
	Read(ctx context.Context) (State, error)
}
