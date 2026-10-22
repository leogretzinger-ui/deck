package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/doctor"
)

func DoctorHandler(cfg *config.Config, listenOwned func() bool) http.Handler {
	reports := newCachedValue(time.Minute, 2*time.Minute, func(ctx context.Context) (doctor.Report, error) {
		return doctor.Run(ctx, cfg, listenOwned()), nil
	})
	return doctorHandler(reports)
}

func doctorHandler(reports *cachedValue[doctor.Report]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		report, err := reports.get(r.Context())
		if err != nil {
			http.Error(w, "The doctor report is not ready. Try again.", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	})
}
