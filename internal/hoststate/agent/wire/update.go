package wire

// The system update service (docs/specs/system-update.md). SystemUpdatePath
// reads its state (GET) and asks it for an operation (POST, a
// systemupdate.Request); SystemUpdateSettingsPath sets the administrator's
// settings (PUT, systemupdate.Settings). The Product Service checks the
// administrator's role.
const (
	SystemUpdatePath         = "/v1/system-update"
	SystemUpdateSettingsPath = "/v1/system-update/settings"
	// UpdateBusyCode refuses a request while the service runs.
	UpdateBusyCode = "update_busy"
)
