package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestStartAllFlagSendsFullSamplePermyriad(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capture/start" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"sample_permyriad":10000,"remaining_seconds":30}`))
	}))
	defer srv.Close()

	if err := run([]string{"start", "--all", "--for", "30s", "--url", srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got["sample_permyriad"] != float64(10000) || got["seconds"] != float64(30) {
		t.Fatalf("unexpected start payload: %+v", got)
	}

	if err := run([]string{"start", "--all", "--sample", "1", "--url", srv.URL}); err == nil {
		t.Fatal("expected error combining --all and --sample 1")
	}
}

func TestSetupDryRunAndDoctorCLI(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{
		"setup",
		"--dry-run",
		"--nginx-conf", filepath.Join(dir, "nginx.conf"),
		"--control-file", filepath.Join(dir, "control"),
		"--db", filepath.Join(dir, "events.sqlite3"),
		"--generation", "r1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"doctor",
		"--nginx-conf", filepath.Join(dir, "nginx.conf"),
		"--control-file", filepath.Join(dir, "control"),
	}); err != nil {
		t.Fatal(err)
	}
}
