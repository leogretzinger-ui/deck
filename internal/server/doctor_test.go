package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/doctor"
)

func TestDoctorHandlerCachesReportAndDisablesBrowserCaching(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "deck.db")
	cfg.Taskmaster.Executable = filepath.Join(t.TempDir(), "noise")
	cfg.Providers = config.Providers{}
	calls := 0
	handler := DoctorHandler(&cfg, func() bool { calls++; return true })
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/doctor", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d, headers=%v", rec.Code, rec.Header())
	}
	var report doctor.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("reported success with missing required tools")
	}
	for range 3 {
		repeated := httptest.NewRecorder()
		handler.ServeHTTP(repeated, httptest.NewRequest(http.MethodGet, "/api/doctor", nil))
		if repeated.Body.String() != rec.Body.String() {
			t.Fatal("repeated request changed the cached report")
		}
	}
	if calls != 1 {
		t.Fatalf("generated %d reports, want 1", calls)
	}
	for _, check := range report.Checks {
		if check.Name == "listen-address" {
			if !check.OK {
				t.Fatalf("refused own listener: %+v", check)
			}
			return
		}
	}
	t.Fatal("missing listener check")
}

func TestDoctorCacheCoalescesRequestsAndRefreshesAfterSixtySeconds(t *testing.T) {
	var now atomic.Int64
	now.Store(100)
	var calls atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	reports := newCachedValue(time.Minute, 2*time.Minute, func(context.Context) (doctor.Report, error) {
		n := calls.Add(1)
		started <- struct{}{}
		<-release
		return doctor.Report{OK: n > 1, Checks: []doctor.Check{}}, nil
	})
	reports.now = func() time.Time { return time.Unix(now.Load(), 0) }
	handler := doctorHandler(reports)
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/doctor", nil))
		return rec
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if rec := request(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
				t.Errorf("initial report: status=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
	<-started
	release <- struct{}{}
	wg.Wait()
	now.Add(59)
	request()
	if calls.Load() != 1 {
		t.Fatal("refreshed report before 60 seconds")
	}
	now.Add(1)
	if rec := request(); !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("did not serve the last report during refresh: %s", rec.Body)
	}
	<-started
	for range 16 {
		wg.Go(func() { request() })
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("generated %d reports, want one initial run and one refresh", calls.Load())
	}
	reports.mu.Lock()
	done := reports.inFlight
	reports.mu.Unlock()
	release <- struct{}{}
	<-done
	if rec := request(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("refreshed report: status=%d body=%s", rec.Code, rec.Body)
	}
}
