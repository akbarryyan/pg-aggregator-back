package handler

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"
	"github.com/google/uuid"
)

// activityExportLimit caps CSV rows, in line with the other admin exports.
const activityExportLimit = 5000

// WithActivityLogs wires the admin activity-log service.
func (h *AdminHandler) WithActivityLogs(svc *service.ActivityLogService) *AdminHandler {
	h.activityLogService = svc
	return h
}

// parseActivityFilter validates the shared list/export query; on failure it
// has already written the 400.
func parseActivityFilter(w http.ResponseWriter, r *http.Request) (activity.Filter, bool) {
	q := r.URL.Query()
	f := activity.Filter{Search: strings.TrimSpace(q.Get("search"))}

	switch at := strings.TrimSpace(q.Get("actor_type")); at {
	case "", activity.ActorAdmin, activity.ActorMerchantUser, activity.ActorAnonymous:
		f.ActorType = at
	default:
		respondError(w, http.StatusBadRequest, "Invalid actor_type")
		return f, false
	}
	switch res := strings.TrimSpace(q.Get("result")); res {
	case "", "success", "failed":
		f.Result = res
	default:
		respondError(w, http.StatusBadRequest, "Invalid result (use success or failed)")
		return f, false
	}
	if raw := strings.TrimSpace(q.Get("merchant_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Invalid merchant_id")
			return f, false
		}
		f.MerchantID = &id
	}
	from, to, ok := parseDateRangeQuery(w, r)
	if !ok {
		return f, false
	}
	f.DateFrom, f.DateTo = from, to
	return f, true
}

func (h *AdminHandler) ListActivityLogs(w http.ResponseWriter, r *http.Request) {
	f, ok := parseActivityFilter(w, r)
	if !ok {
		return
	}
	limit, offset := parseLimitOffset(r, 20, 0)
	items, total, err := h.activityLogService.List(r.Context(), f, limit, offset)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to list activity logs: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to list activity logs")
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func (h *AdminHandler) GetActivityLogStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.activityLogService.Stats(r.Context())
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to load activity log stats: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to load activity log stats")
		return
	}
	respondJSON(w, http.StatusOK, stats)
}

func (h *AdminHandler) ExportActivityLogs(w http.ResponseWriter, r *http.Request) {
	f, ok := parseActivityFilter(w, r)
	if !ok {
		return
	}
	extendWriteDeadline(w, exportWriteTimeout)
	rows, err := h.activityLogService.ListForExport(r.Context(), f, activityExportLimit)
	if err != nil {
		logger.ErrorfCtx(r.Context(), "Failed to export activity logs: %v", err)
		respondError(w, http.StatusInternalServerError, "Failed to export activity logs")
		return
	}

	filename := fmt.Sprintf("activity-logs-%s.csv", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{
		"time", "actor_type", "actor_email", "merchant_id", "action", "method", "route",
		"target_id", "status_code", "ip", "user_agent", "duration_ms", "details",
	})
	for _, e := range rows {
		merchantID := ""
		if e.MerchantID != nil {
			merchantID = e.MerchantID.String()
		}
		details := ""
		if len(e.Details) > 0 {
			if b, err := json.Marshal(e.Details); err == nil {
				details = string(b)
			}
		}
		_ = writer.Write([]string{
			e.CreatedAt.UTC().Format(time.RFC3339), csvSafe(e.ActorType), csvSafe(e.ActorEmail), merchantID,
			csvSafe(e.Action), csvSafe(e.Method), csvSafe(e.Route), csvSafe(e.TargetID), strconv.Itoa(e.StatusCode),
			csvSafe(e.IP), csvSafe(e.UserAgent), strconv.Itoa(e.DurationMS), csvSafe(details),
		})
	}
	writer.Flush()
}

// csvSafe neutralises spreadsheet formula injection by prefixing a single quote
// to cells that start with a formula trigger character.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
