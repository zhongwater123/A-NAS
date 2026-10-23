// Package systemlogapi serves the allowlisted A-NAS service logs to
// administrators (docs/specs/logs.md). The filters are validated here and
// again by the Host Agent, which reads the journal (ADR 0020).
package systemlogapi

import (
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

// API reads logs from reader; a nil reader answers logs_unavailable.
// dataSource tells the page whether the logs are simulated or live.
type API struct {
	reader     systemlog.Reader
	dataSource string
	logger     *slog.Logger
}

func New(reader systemlog.Reader, dataSource string, logger *slog.Logger) *API {
	return &API{reader: reader, dataSource: dataSource, logger: logger}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/system-logs", Posture: apiroute.Admin, Handler: a.logs},
	}
}

type response struct {
	systemlog.Page
	DataSource string `json:"dataSource"`
}

func (a *API) logs(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if a.reader == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "logs_unavailable", "system logs are unavailable")
		return
	}
	q, err := systemlog.ParseQuery(r.URL.Query())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "system log filters are invalid")
		return
	}
	page, err := a.reader.ReadLogs(r.Context(), q)
	if err != nil {
		a.logger.WarnContext(r.Context(), "system logs unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "logs_unavailable", "system logs cannot be read right now")
		return
	}
	httpjson.Write(w, http.StatusOK, response{Page: page, DataSource: a.dataSource})
}
