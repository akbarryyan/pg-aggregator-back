package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/activity"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/google/uuid"
)

type activityFake struct {
	entries []activity.Entry
	total   int64
	err     error
	gotF    activity.Filter
	gotLim  int
	gotOff  int
}

func (f *activityFake) List(ctx context.Context, fl activity.Filter, limit, offset int) ([]activity.Entry, error) {
	f.gotF, f.gotLim, f.gotOff = fl, limit, offset
	return f.entries, f.err
}
func (f *activityFake) ListForExport(ctx context.Context, fl activity.Filter, limit int) ([]activity.Entry, error) {
	f.gotF, f.gotLim, f.gotOff = fl, limit, 0
	return f.entries, f.err
}
func (f *activityFake) Count(ctx context.Context, fl activity.Filter) (int64, error) {
	return f.total, f.err
}
func (f *activityFake) Stats(ctx context.Context, days int) (*activity.Stats, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &activity.Stats{Days: days, Total: 1}, nil
}

func newActivityHandler(f *activityFake) *AdminHandler {
	return NewAdminHandler(nil, "http://localhost:3000").WithActivityLogs(service.NewActivityLogService(f))
}

func getReq(fn http.HandlerFunc, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	fn(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestListActivityLogs_Validation(t *testing.T) {
	h := newActivityHandler(&activityFake{})
	for _, q := range []string{
		"actor_type=root", "merchant_id=nope", "result=maybe", "date_from=2026-13-01", "date_from=2026-11-05&date_to=2026-11-01",
	} {
		if rec := getReq(h.ListActivityLogs, "/x?"+q); rec.Code != 400 {
			t.Errorf("%s -> %d, want 400", q, rec.Code)
		}
		if rec := getReq(h.ExportActivityLogs, "/x?"+q); rec.Code != 400 {
			t.Errorf("export %s -> %d, want 400", q, rec.Code)
		}
	}
}

func TestListActivityLogs_OKAndFilters(t *testing.T) {
	f := &activityFake{total: 7}
	h := newActivityHandler(f)
	mid := uuid.New()
	rec := getReq(h.ListActivityLogs, "/x?search=%20bob%20&actor_type=admin&merchant_id="+mid.String()+"&result=failed&date_from=2026-11-01&date_to=2026-11-02&limit=10&offset=20")
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	if f.gotF.Search != "bob" || f.gotF.ActorType != "admin" || f.gotF.Result != "failed" ||
		f.gotF.MerchantID == nil || *f.gotF.MerchantID != mid || f.gotF.DateFrom == nil || f.gotF.DateTo == nil ||
		f.gotLim != 10 || f.gotOff != 20 {
		t.Fatalf("filter = %+v %d %d", f.gotF, f.gotLim, f.gotOff)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["items"]) != "[]" || string(body["total"]) != "7" || string(body["limit"]) != "10" || string(body["offset"]) != "20" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestListActivityLogs_ServiceError(t *testing.T) {
	h := newActivityHandler(&activityFake{err: errors.New("db")})
	if rec := getReq(h.ListActivityLogs, "/x"); rec.Code != 500 || strings.Contains(rec.Body.String(), "db") {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetActivityLogStats(t *testing.T) {
	h := newActivityHandler(&activityFake{})
	rec := getReq(h.GetActivityLogStats, "/x")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"days":14`) {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	if rec := getReq(newActivityHandler(&activityFake{err: errors.New("x")}).GetActivityLogStats, "/x"); rec.Code != 500 {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestExportActivityLogs_CSV(t *testing.T) {
	mid := uuid.New()
	f := &activityFake{entries: []activity.Entry{{
		CreatedAt: time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC), ActorType: "admin", ActorEmail: "a@x.io",
		MerchantID: &mid, Action: "Viewed payments", Method: "GET", Route: "/api/v1/admin/payments",
		TargetID: "t1", StatusCode: 200, IP: "1.2.3.4", UserAgent: "UA", DurationMS: 12,
		Details: map[string]interface{}{"k": "v"},
	}}}
	rec := getReq(newActivityHandler(f).ExportActivityLogs, "/x")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("code %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "activity-logs-") {
		t.Fatalf("disposition %q", rec.Header().Get("Content-Disposition"))
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d: %s", len(lines), rec.Body.String())
	}
	if lines[0] != "time,actor_type,actor_email,merchant_id,action,method,route,target_id,status_code,ip,user_agent,duration_ms,details" {
		t.Fatalf("header = %s", lines[0])
	}
	if !strings.Contains(lines[1], "2026-11-13T01:02:03Z,admin,a@x.io,"+mid.String()+",Viewed payments,GET,") ||
		!strings.Contains(lines[1], `"{""k"":""v""}"`) {
		t.Fatalf("row = %s", lines[1])
	}
	if f.gotLim != activityExportLimit || f.gotOff != 0 {
		t.Fatalf("limit %d offset %d", f.gotLim, f.gotOff)
	}
}

func TestExportActivityLogs_NeutralisesFormulas(t *testing.T) {
	f := &activityFake{entries: []activity.Entry{{
		CreatedAt: time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC), ActorType: "anonymous",
		ActorEmail: `=HYPERLINK("x")`, Action: "+cmd", Method: "POST", Route: "-r", TargetID: "\tx",
		StatusCode: 401, UserAgent: "@SUM(1)", IP: "1.2.3.4",
	}}}
	rec := getReq(newActivityHandler(f).ExportActivityLogs, "/x")
	recs, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("parse: %v %v", err, recs)
	}
	row := recs[1]
	if row[2] != `'=HYPERLINK("x")` || row[10] != "'@SUM(1)" || row[4] != "'+cmd" || row[6] != "'-r" || row[7] != "'\tx" {
		t.Fatalf("row = %q", row)
	}
	if row[1] != "anonymous" || row[8] != "401" || row[9] != "1.2.3.4" {
		t.Fatalf("safe cells altered: %q", row)
	}
}
