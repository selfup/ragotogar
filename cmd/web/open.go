package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// openExecutor runs /usr/bin/open. Swappable in tests so unit runs never
// spawn a real macOS app launch.
var openExecutor = func(args ...string) ([]byte, error) {
	return exec.Command("/usr/bin/open", args...).CombinedOutput()
}

// statFile verifies the original file is still on disk before handing
// its path to `open`. Swappable in tests so we don't need real files
// at the seeded /some/path/... fixture paths.
var statFile = func(path string) error {
	_, err := os.Stat(path)
	return err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// openArgs builds the /usr/bin/open argv for the requested app. Returns
// nil for unknown app tokens. App-name resolution honors DXO_APP_NAME /
// CAPTUREONE_APP_NAME — `open -a` matches the on-disk bundle name (e.g.
// `DxO PhotoLab 9.app`). Capture One's bundle is unversioned so the
// default works across 16.x builds; DxO bumps the bundle name on each
// major release, so override the env var after upgrading.
func openArgs(app, path string) []string {
	switch app {
	case "dxo":
		return []string{"-a", envOr("DXO_APP_NAME", "DxO PhotoLab 9"), path}
	case "c1":
		return []string{"-a", envOr("CAPTUREONE_APP_NAME", "Capture One"), path}
	case "finder":
		return []string{"-R", path}
	}
	return nil
}

// servePhotoOpen handles POST /photos/<name>/open?app=<dxo|c1|finder>.
// Looks up the original disk path from photos.file_path, verifies it,
// then shells out to /usr/bin/open. POST-only so a stray <a href> or
// link prefetch can't trigger an app launch.
func servePhotoOpen(w http.ResponseWriter, r *http.Request, db *sql.DB, name string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if name == "" || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	app := r.URL.Query().Get("app")
	if openArgs(app, "x") == nil {
		http.Error(w, "unknown app — use dxo|c1|finder", http.StatusBadRequest)
		return
	}

	var path string
	err := db.QueryRow(
		"SELECT COALESCE(file_path, '') FROM photos WHERE name = $1", name,
	).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if path == "" {
		http.Error(w, "no file_path on record for "+name, http.StatusNotFound)
		return
	}
	if err := statFile(path); err != nil {
		http.Error(w, fmt.Sprintf("file not on disk: %s", path), http.StatusGone)
		return
	}

	out, err := openExecutor(openArgs(app, path)...)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"app":%q,"path":%q}`, app, path)
}
