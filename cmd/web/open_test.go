package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// withStubExec swaps openExecutor + statFile for tests, restoring the
// originals on cleanup. Callers can inspect the captured argv after the
// handler runs.
type capturedExec struct {
	args []string
}

func withStubExec(t *testing.T, statErr error, execOut []byte, execErr error) *capturedExec {
	t.Helper()
	cap := &capturedExec{}
	origExec, origStat := openExecutor, statFile
	openExecutor = func(args ...string) ([]byte, error) {
		cap.args = append([]string{}, args...)
		return execOut, execErr
	}
	statFile = func(path string) error { return statErr }
	t.Cleanup(func() {
		openExecutor = origExec
		statFile = origStat
	})
	return cap
}

func TestOpenArgs(t *testing.T) {
	t.Setenv("DXO_APP_NAME", "")
	t.Setenv("CAPTUREONE_APP_NAME", "")
	cases := []struct {
		app, path string
		want      []string
	}{
		{"dxo", "/p.jpg", []string{"-a", "DxO PhotoLab 9", "/p.jpg"}},
		{"c1", "/p.jpg", []string{"-a", "Capture One", "/p.jpg"}},
		{"finder", "/p.jpg", []string{"-R", "/p.jpg"}},
		{"unknown", "/p.jpg", nil},
		{"", "/p.jpg", nil},
	}
	for _, tc := range cases {
		got := openArgs(tc.app, tc.path)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("openArgs(%q) = %v, want %v", tc.app, got, tc.want)
		}
	}
}

func TestOpenArgsHonorsEnvOverride(t *testing.T) {
	t.Setenv("DXO_APP_NAME", "DxO PhotoLab 10")
	t.Setenv("CAPTUREONE_APP_NAME", "Capture One Pro")
	if got := openArgs("dxo", "/x"); !reflect.DeepEqual(got, []string{"-a", "DxO PhotoLab 10", "/x"}) {
		t.Errorf("DXO override not applied: %v", got)
	}
	if got := openArgs("c1", "/x"); !reflect.DeepEqual(got, []string{"-a", "Capture One Pro", "/x"}) {
		t.Errorf("C1 override not applied: %v", got)
	}
}

func TestServePhotoOpenMethodNotAllowed(t *testing.T) {
	db := newTestDB(t)
	withStubExec(t, nil, nil, nil)

	rr := httptest.NewRecorder()
	servePhotoOpen(rr, httptest.NewRequest(http.MethodGet, "/photos/x/open", nil), db, "x")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
	if rr.Header().Get("Allow") != "POST" {
		t.Errorf("Allow header = %q", rr.Header().Get("Allow"))
	}
}

func TestServePhotoOpenRejectsPathTraversal(t *testing.T) {
	db := newTestDB(t)
	withStubExec(t, nil, nil, nil)

	rr := httptest.NewRecorder()
	servePhotoOpen(rr, httptest.NewRequest(http.MethodPost, "/photos/.%2e/etc/open", nil), db, "../etc")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for slash in name", rr.Code)
	}
}

func TestServePhotoOpenUnknownApp(t *testing.T) {
	db := newTestDB(t)
	seedPhoto(t, db, "p1", []byte{0xff})
	withStubExec(t, nil, nil, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/photos/p1/open?app=lightroom", nil)
	servePhotoOpen(rr, req, db, "p1")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestServePhotoOpenMissingPhoto(t *testing.T) {
	db := newTestDB(t)
	withStubExec(t, nil, nil, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/photos/nope/open?app=dxo", nil)
	servePhotoOpen(rr, req, db, "nope")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestServePhotoOpenEmptyFilePath(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec(
		"INSERT INTO photos (id, name, file_path, file_basename) VALUES ($1, $2, NULL, $3)",
		"orphan", "orphan", "orphan.jpg",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	withStubExec(t, nil, nil, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/photos/orphan/open?app=dxo", nil)
	servePhotoOpen(rr, req, db, "orphan")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no file_path") {
		t.Errorf("missing helpful error: %s", rr.Body.String())
	}
}

func TestServePhotoOpenFileMissingOnDisk(t *testing.T) {
	db := newTestDB(t)
	seedPhoto(t, db, "p1", []byte{0xff})
	withStubExec(t, os.ErrNotExist, nil, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/photos/p1/open?app=dxo", nil)
	servePhotoOpen(rr, req, db, "p1")
	if rr.Code != http.StatusGone {
		t.Errorf("status = %d, want 410; body=%s", rr.Code, rr.Body.String())
	}
}

func TestServePhotoOpenHappyPath(t *testing.T) {
	db := newTestDB(t)
	seedPhoto(t, db, "p1", []byte{0xff})
	t.Setenv("DXO_APP_NAME", "DxO PhotoLab 9")
	cap := withStubExec(t, nil, []byte("ok"), nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/photos/p1/open?app=dxo", nil)
	servePhotoOpen(rr, req, db, "p1")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %s", got)
	}
	wantArgs := []string{"-a", "DxO PhotoLab 9", "/some/path/p1.jpg"}
	if !reflect.DeepEqual(cap.args, wantArgs) {
		t.Errorf("open argv = %v, want %v", cap.args, wantArgs)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"app":"dxo"`) {
		t.Errorf("response body = %s", body)
	}
}

func TestServePhotoOpenFinderHappyPath(t *testing.T) {
	db := newTestDB(t)
	seedPhoto(t, db, "p1", []byte{0xff})
	cap := withStubExec(t, nil, []byte(""), nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/photos/p1/open?app=finder", nil)
	servePhotoOpen(rr, req, db, "p1")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	wantArgs := []string{"-R", "/some/path/p1.jpg"}
	if !reflect.DeepEqual(cap.args, wantArgs) {
		t.Errorf("finder argv = %v, want %v", cap.args, wantArgs)
	}
}

func TestServePhotoOpenSurfacesExecError(t *testing.T) {
	db := newTestDB(t)
	seedPhoto(t, db, "p1", []byte{0xff})
	withStubExec(t, nil, []byte("Unable to find application named 'DxO PhotoLab 9'"), errors.New("exit 1"))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/photos/p1/open?app=dxo", nil)
	servePhotoOpen(rr, req, db, "p1")

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Unable to find application") {
		t.Errorf("expected stderr surfaced, got %q", rr.Body.String())
	}
}
