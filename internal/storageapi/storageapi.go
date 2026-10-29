// Package storageapi is the HTTP side of internal/storage: the data volume,
// the plans that create or grow it (which erase disks, so only
// administrators reach them) and the operations that carry them out.
package storageapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

// API serves the storage service; a nil service answers 503
// storage_unavailable.
type API struct {
	storage *storage.Service
	logger  *slog.Logger
}

func New(service *storage.Service, logger *slog.Logger) *API {
	return &API{storage: service, logger: logger}
}

func (a *API) Routes() []apiroute.Route {
	get, post := http.MethodGet, http.MethodPost
	admin := apiroute.Admin
	return []apiroute.Route{
		{Method: get, Pattern: "/api/v1/volumes", Posture: apiroute.Session, Handler: a.available(a.volumes)},
		{Method: post, Pattern: "/api/v1/volumes/{volumeID}/conversion", Posture: admin, Handler: a.available(a.continueConversion)},
		{Method: get, Pattern: "/api/v1/storage/plans", Posture: admin, Handler: a.available(a.unfinishedPlans)},
		{Method: post, Pattern: "/api/v1/storage/plans", Posture: admin, Handler: a.available(a.createPlan)},
		{Method: post, Pattern: "/api/v1/storage/plans/{planID}/confirm", Posture: admin, Handler: a.available(a.confirmPlan)},
		{Method: post, Pattern: "/api/v1/storage/plans/{planID}/execute", Posture: admin, Handler: a.available(a.executePlan)},
		{Method: post, Pattern: "/api/v1/storage/plans/{planID}/cancel", Posture: admin, Handler: a.available(a.cancelPlan)},
		{Method: get, Pattern: "/api/v1/operations/{operationID}", Posture: admin, Handler: a.available(a.operation)},
	}
}

// available answers storage_unavailable without a storage service. The
// session's user goes on to the service, which checks the administrator
// itself on everything that plans or changes the volume (#270).
func (a *API) available(next apiroute.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if a.storage == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
			return
		}
		next(w, r, session)
	}
}

func (a *API) createPlan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		DiskIDs []string       `json:"diskIds"`
		Layout  storage.Layout `json:"layout"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	plan, err := a.storage.PlanVolume(r.Context(), session.User, request.DiskIDs, request.Layout)
	if err != nil {
		WriteError(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, plan)
}

func (a *API) volumes(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	volumes, err := a.storage.ListVolumes(r.Context())
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	if volumes == nil {
		volumes = []storage.Volume{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": volumes})
}

// continueConversion starts converting the data volume to its layout now,
// after a shutdown or an error stopped the conversion.
func (a *API) continueConversion(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	volumes, err := a.storage.ListVolumes(r.Context())
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	if len(volumes) == 0 || volumes[0].ID != r.PathValue("volumeID") {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "storage resource was not found")
		return
	}
	if err := a.storage.ContinueConversion(r.Context(), session.User); err != nil {
		WriteError(w, r, a.logger, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) confirmPlan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ConfirmationPhrase string `json:"confirmationPhrase"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	plan, err := a.storage.ConfirmPlan(r.Context(), session.User, r.PathValue("planID"), request.ConfirmationPhrase)
	if err != nil {
		WriteError(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, plan)
}

func (a *API) executePlan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	plan, err := a.storage.ExecutePlan(r.Context(), session.User, r.PathValue("planID"))
	if err != nil {
		WriteError(w, r, a.logger, err)
		return
	}
	// The Host Agent goes on with a running plan after this response;
	// GET /api/v1/operations/{planId} follows it.
	status := http.StatusOK
	if plan.State == storage.PlanStateRunning {
		status = http.StatusAccepted
	}
	httpjson.Write(w, status, plan)
}

// unfinishedPlans lists the plans that still need the administrator, so
// that the settings page finds them again after a reload.
func (a *API) unfinishedPlans(w http.ResponseWriter, _ *http.Request, _ accounts.Session) {
	httpjson.Write(w, http.StatusOK, map[string]any{"items": a.storage.UnfinishedPlans()})
}

func (a *API) cancelPlan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	plan, err := a.storage.CancelPlan(r.Context(), session.User, r.PathValue("planID"))
	if err != nil {
		WriteError(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, plan)
}

func (a *API) operation(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	plan, err := a.storage.GetPlan(r.PathValue("operationID"))
	if err != nil {
		WriteError(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, plan)
}

// WriteError answers an error of the storage service.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, accounts.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "administrator permission is required")
	case errors.Is(err, storage.ErrDiskNotFound), errors.Is(err, storage.ErrPlanNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "storage resource was not found")
	case errors.Is(err, storage.ErrDiskNotEligible):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "disk_not_eligible", err.Error())
	case errors.Is(err, storage.ErrConfirmation):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "confirmation_mismatch", err.Error())
	case errors.Is(err, storage.ErrPlanExpired):
		httpjson.WriteError(w, http.StatusGone, "plan_expired", err.Error())
	case errors.Is(err, storage.ErrPlanState):
		httpjson.WriteError(w, http.StatusConflict, "plan_state_conflict", err.Error())
	case errors.Is(err, storage.ErrVolumeUnavailable):
		// 423, as every API answers volume_unavailable (#222).
		httpjson.WriteError(w, http.StatusLocked, "volume_unavailable", err.Error())
	case errors.Is(err, storage.ErrVolumeBusy):
		httpjson.WriteError(w, http.StatusConflict, "volume_busy", err.Error())
	case errors.Is(err, storage.ErrVolumeExists):
		httpjson.WriteError(w, http.StatusConflict, "volume_exists", err.Error())
	case errors.Is(err, storage.ErrNoConversion):
		httpjson.WriteError(w, http.StatusConflict, "no_conversion", err.Error())
	case errors.Is(err, storage.ErrInvalidLayout):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "invalid_layout", err.Error())
	case errors.Is(err, storage.ErrLayoutTooSmall):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "layout_too_small", err.Error())
	case errors.Is(err, storage.ErrOperationAttention), errors.Is(err, storage.ErrDiskChanged):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_needs_attention", err.Error())
	default:
		httpjson.WriteInternal(w, r, logger, err)
	}
}
