package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// handleBrowseAPI lists sub-directories of ?path= (folders only, no files)
// so the settings page's "폴더 찾아보기" modal can navigate the server's
// filesystem instead of the operator having to type an exact path by hand.
// Gated behind requireAuth like every other /api route — this is local
// admin tooling, not a public listing.
func (s *Server) handleBrowseAPI(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")

	resp := struct {
		Path   string   `json:"path"`
		Parent string   `json:"parent"`
		Drives []string `json:"drives"`
		Dirs   []string `json:"dirs"`
		Error  string   `json:"error,omitempty"`
	}{Drives: listDrives()}

	if reqPath == "" {
		if wd, err := os.Getwd(); err == nil {
			reqPath = wd
		}
	}

	abs, err := filepath.Abs(reqPath)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, resp)
		return
	}
	resp.Path = abs

	if parent := filepath.Dir(abs); parent != abs {
		resp.Parent = parent
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, resp)
		return
	}

	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "$") { // skip Windows system dirs like $RECYCLE.BIN
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, name)
			continue
		}
		// A reparse point (junction/symlink) reports IsDir()==false from
		// ReadDir's cached type bit; Stat resolves it to check for real.
		if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(abs, name)); err == nil && fi.IsDir() {
				dirs = append(dirs, name)
			}
		}
	}
	sort.Strings(dirs)
	resp.Dirs = dirs

	writeJSON(w, resp)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
