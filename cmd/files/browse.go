package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed web
var assets embed.FS

type browser struct {
	config config
	root   *os.Root
	log    *slog.Logger
}

func newBrowser(cfg config, root *os.Root, logger *slog.Logger) *browser {
	return &browser{config: cfg, root: root, log: logger}
}

func (b *browser) handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /healthz", b.health)
	mux.HandleFunc("GET /api/list", b.list)
	mux.HandleFunc("GET /api/file", b.download)
	mux.HandleFunc("POST /api/upload", b.upload)
	mux.HandleFunc("POST /api/mkdir", b.mkdir)
	mux.HandleFunc("POST /api/delete", b.remove)

	var handler http.Handler = mux
	if b.config.basePath != "" {
		// The controller proxies this app without stripping its prefix, so the
		// base path arrives intact and is removed here.
		handler = http.StripPrefix(b.config.basePath, handler)
	}
	return securityHeaders(handler)
}

func (b *browser) health(w http.ResponseWriter, r *http.Request) {
	if _, err := b.root.Stat("."); err != nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"status": "unhealthy", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Entry is one row in a directory listing.
type Entry struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Dir      bool      `json:"dir"`
	// Kind drives the preview affordance in the UI: dir, image, video, audio,
	// text, or other.
	Kind string `json:"kind"`
}

type listing struct {
	Path    string  `json:"path"`
	Parent  string  `json:"parent"`
	Entries []Entry `json:"entries"`
}

// cleanPath converts a request's ?path value into a slash-separated path
// relative to the workspace root, or reports that it is unusable.
//
// os.Root already refuses to escape, but normalising here means the rest of the
// code works with one predictable shape and the UI gets a clear error instead
// of a generic open failure.
func cleanPath(raw string) (string, error) {
	if raw == "" || raw == "/" {
		return ".", nil
	}
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("path contains a null byte")
	}
	cleaned := path.Clean("/" + strings.ReplaceAll(raw, `\`, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		return ".", nil
	}
	// Anchoring at "/" before cleaning means surplus ".." are discarded rather
	// than climbing above the root, so this branch is unreachable today. It is
	// kept because it becomes load-bearing the moment someone cleans the raw
	// value instead of the anchored one, and os.Root is the actual enforcement
	// either way.
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path escapes the workspace")
	}
	return cleaned, nil
}

func (b *browser) list(w http.ResponseWriter, r *http.Request) {
	target, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, err := b.root.Open(target)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("not a directory"))
		return
	}
	items, err := file.ReadDir(-1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result := listing{Path: displayPath(target), Parent: parentOf(target)}
	result.Entries = make([]Entry, 0, len(items))
	for _, item := range items {
		entry := Entry{Name: item.Name(), Dir: item.IsDir()}
		if details, err := item.Info(); err == nil {
			entry.Size = details.Size()
			entry.Modified = details.ModTime().UTC()
		}
		entry.Kind = "other"
		if entry.Dir {
			entry.Kind = "dir"
		} else {
			entry.Kind = kindOf(item.Name())
		}
		result.Entries = append(result.Entries, entry)
	}
	// Directories first, then names, so the listing is stable regardless of
	// the order the filesystem happened to return.
	sort.Slice(result.Entries, func(i, j int) bool {
		if result.Entries[i].Dir != result.Entries[j].Dir {
			return result.Entries[i].Dir
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})
	writeJSON(w, http.StatusOK, result)
}

func (b *browser) download(w http.ResponseWriter, r *http.Request) {
	target, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if target == "." {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	file, err := b.root.Open(target)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("path is a directory"))
		return
	}

	name := path.Base(target)
	kind := kindOf(name)
	inline := r.URL.Query().Get("disposition") != "attachment" && previewable(kind)
	if inline {
		w.Header().Set("Content-Type", contentType(name, kind))
		w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+urlEncode(name))
	} else {
		// Anything not on the preview allowlist leaves as an opaque download.
		// This is what stops an uploaded .html or .svg from executing against
		// the controller's origin.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEncode(name))
	}
	// ServeContent handles Range requests, which is what makes video seeking
	// work rather than forcing a full download before playback.
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (b *browser) upload(w http.ResponseWriter, r *http.Request) {
	target, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, b.config.maxUploadBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	written := make([]string, 0, 4)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		// Use only the base name: a browser may send a path, and a crafted
		// client certainly will.
		name := filepath.Base(filepath.FromSlash(part.FileName()))
		if name == "." || name == string(filepath.Separator) || name == ".." {
			part.Close()
			writeError(w, http.StatusBadRequest, errors.New("invalid upload file name"))
			return
		}
		destination := name
		if target != "." {
			destination = target + "/" + name
		}
		if err := b.writeFile(destination, part); err != nil {
			part.Close()
			writeError(w, statusFor(err), err)
			return
		}
		part.Close()
		written = append(written, name)
	}
	if len(written) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("no files were uploaded"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uploaded": written})
}

func (b *browser) writeFile(destination string, source io.Reader) error {
	file, err := b.root.OpenFile(destination,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, source); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (b *browser) mkdir(w http.ResponseWriter, r *http.Request) {
	target, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil || target == "." {
		writeError(w, http.StatusBadRequest, errors.New("a folder path is required"))
		return
	}
	if err := b.root.Mkdir(target, 0o755); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"created": displayPath(target)})
}

func (b *browser) remove(w http.ResponseWriter, r *http.Request) {
	target, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil || target == "." {
		writeError(w, http.StatusBadRequest, errors.New("a path is required"))
		return
	}
	if err := b.root.Remove(target); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"removed": displayPath(target)})
}

// previewable lists the kinds served inline. Everything else downloads.
func previewable(kind string) bool {
	switch kind {
	case "image", "video", "audio", "text":
		return true
	}
	return false
}

func kindOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".avif":
		return "image"
	case ".mp4", ".webm", ".mkv", ".mov", ".m4v":
		return "video"
	case ".mp3", ".ogg", ".wav", ".flac", ".m4a":
		return "audio"
	case ".txt", ".md", ".log", ".json", ".yaml", ".yml", ".csv", ".ini", ".conf":
		return "text"
	}
	return "other"
}

// contentType returns a type safe to serve inline. SVG is deliberately absent
// from the image list in kindOf because it can carry script.
func contentType(name, kind string) string {
	if kind == "text" {
		return "text/plain; charset=utf-8"
	}
	if guessed := mime.TypeByExtension(strings.ToLower(path.Ext(name))); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

func displayPath(target string) string {
	if target == "." {
		return "/"
	}
	return "/" + target
}

func parentOf(target string) string {
	if target == "." {
		return ""
	}
	parent := path.Dir(target)
	if parent == "." {
		return "/"
	}
	return "/" + parent
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden
	case errors.Is(err, fs.ErrExist):
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func urlEncode(name string) string {
	var out strings.Builder
	for _, b := range []byte(name) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '-' || b == '.' || b == '_' || b == '~' {
			out.WriteByte(b)
			continue
		}
		out.WriteByte('%')
		const hex = "0123456789ABCDEF"
		out.WriteByte(hex[b>>4])
		out.WriteByte(hex[b&0x0f])
	}
	return out.String()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasSuffix(r.URL.Path, "/api/file") {
			// File bytes are untrusted workspace content. Even for the types
			// served inline, deny every capability a document could use.
			w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		} else {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; img-src 'self' blob:; media-src 'self' blob:; "+
					"style-src 'self'; script-src 'self'; connect-src 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
