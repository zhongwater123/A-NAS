package updater

// The contract's names, as this package uses them.

import "github.com/zhongwater123/A-NAS/internal/systemupdate"

type (
	Phase    = systemupdate.Phase
	Problem  = systemupdate.Problem
	Settings = systemupdate.Settings
	Record   = systemupdate.Record
	State    = systemupdate.State
	Request  = systemupdate.Request
	Status   = systemupdate.Status
)

const (
	PhaseUpToDate       = systemupdate.PhaseUpToDate
	PhaseAvailable      = systemupdate.PhaseAvailable
	PhaseDownloading    = systemupdate.PhaseDownloading
	PhaseReady          = systemupdate.PhaseReady
	PhaseInstalling     = systemupdate.PhaseInstalling
	PhaseRollingBack    = systemupdate.PhaseRollingBack
	PhaseRolledBack     = systemupdate.PhaseRolledBack
	PhaseCheckFailed    = systemupdate.PhaseCheckFailed
	ProblemUnreachable  = systemupdate.ProblemUnreachable
	ProblemSignature    = systemupdate.ProblemSignature
	ProblemDiskSpace    = systemupdate.ProblemDiskSpace
	ProblemDownload     = systemupdate.ProblemDownload
	ProblemInstall      = systemupdate.ProblemInstall
	ProblemCheck        = systemupdate.ProblemCheck
	ProblemRollback     = systemupdate.ProblemRollback
	ProblemStaging      = systemupdate.ProblemStaging
	ProblemNotInstalled = systemupdate.ProblemNotInstalled
	RecordUpdated       = systemupdate.RecordUpdated
	RecordRolledBack    = systemupdate.RecordRolledBack
	RecordReverted      = systemupdate.RecordReverted
	RecordCheckFailed   = systemupdate.RecordCheckFailed
	RecordDownloaded    = systemupdate.RecordDownloaded
	RecordFailed        = systemupdate.RecordFailed
	RequestTick         = systemupdate.RequestTick
	RequestCheck        = systemupdate.RequestCheck
	RequestDownload     = systemupdate.RequestDownload
	RequestInstall      = systemupdate.RequestInstall
	RequestRevert       = systemupdate.RequestRevert
	RequestBoot         = systemupdate.RequestBoot
	ByAutomatic         = systemupdate.ByAutomatic
)

var (
	DefaultSettings = systemupdate.DefaultSettings
	Requestable     = systemupdate.Requestable
)
