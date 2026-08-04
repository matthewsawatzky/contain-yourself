package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBrowser(t *testing.T) (*browser, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.txt", "hello workspace")
	write("clip.mp4", strings.Repeat("v", 4096))
	write("photo.png", "not really a png")
	write("payload.html", "<script>alert(document.cookie)</script>")
	write("drawing.svg", "<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")
	write("projects/readme.md", "# project")

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	cfg := config{root: dir, basePath: "", maxUploadBytes: 1 << 20}
	return newBrowser(cfg, root, slog.Default()), dir
}

func do(t *testing.T, b *browser, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, body)
	recorder := httptest.NewRecorder()
	b.handler().ServeHTTP(recorder, request)
	return recorder
}

func TestListReturnsDirectoriesFirstAndClassifiesKinds(t *testing.T) {
	b, _ := testBrowser(t)
	recorder := do(t, b, http.MethodGet, "/api/list?path=/", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result listing
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != "/" || result.Parent != "" {
		t.Fatalf("path/parent = %q/%q", result.Path, result.Parent)
	}
	if !result.Entries[0].Dir {
		t.Fatalf("directories should sort first, got %+v", result.Entries[0])
	}
	kinds := map[string]string{}
	for _, entry := range result.Entries {
		kinds[entry.Name] = entry.Kind
	}
	for name, want := range map[string]string{
		"projects": "dir", "notes.txt": "text", "clip.mp4": "video",
		"photo.png": "image", "payload.html": "other",
		// SVG can carry script, so it is deliberately not an "image".
		"drawing.svg": "other",
	} {
		if kinds[name] != want {
			t.Errorf("kind of %s = %q, want %q", name, kinds[name], want)
		}
	}
}

// os.Root is the real defence, but the request path is normalised first so the
// UI gets a clear error rather than an opaque open failure.
func TestPathsCannotEscapeTheWorkspace(t *testing.T) {
	b, dir := testBrowser(t)
	secret := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(secret, []byte("do not serve me"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })

	for _, attempt := range []string{
		"/../outside.txt", "../outside.txt", "/../../etc/passwd",
		"..%2F..%2Fetc%2Fpasswd", `\..\outside.txt`, "/./../outside.txt",
	} {
		recorder := do(t, b, http.MethodGet, "/api/file?path="+attempt, nil)
		if recorder.Code == http.StatusOK {
			t.Errorf("path %q was served: %s", attempt, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "do not serve me") {
			t.Fatalf("path %q leaked content outside the workspace", attempt)
		}
	}
}

// A symlink planted inside the workspace is the case plain string checks miss.
func TestSymlinksOutOfTheWorkspaceAreRefused(t *testing.T) {
	b, dir := testBrowser(t)
	secret := filepath.Join(filepath.Dir(dir), "linked-secret.txt")
	if err := os.WriteFile(secret, []byte("symlink target"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })
	if err := os.Symlink(secret, filepath.Join(dir, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	recorder := do(t, b, http.MethodGet, "/api/file?path=/escape.txt", nil)
	if recorder.Code == http.StatusOK {
		t.Fatalf("a symlink out of the workspace was served: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "symlink target") {
		t.Fatal("symlink content leaked")
	}
}

// This is the one that stops an uploaded page from running script against the
// controller's session cookie: app traffic shares the controller's origin.
func TestOnlyAllowlistedTypesAreServedInline(t *testing.T) {
	b, _ := testBrowser(t)
	cases := map[string]bool{
		"/photo.png": true, "/clip.mp4": true, "/notes.txt": true,
		"/payload.html": false, "/drawing.svg": false,
	}
	for path, wantInline := range cases {
		recorder := do(t, b, http.MethodGet, "/api/file?path="+path, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, recorder.Code)
		}
		disposition := recorder.Header().Get("Content-Disposition")
		inline := strings.HasPrefix(disposition, "inline")
		if inline != wantInline {
			t.Errorf("%s disposition = %q, want inline=%v", path, disposition, wantInline)
		}
		if !wantInline {
			if got := recorder.Header().Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("%s content type = %q, want octet-stream", path, got)
			}
		}
		if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s is missing nosniff", path)
		}
		if csp := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
			t.Errorf("%s CSP = %q, want a sandbox", path, csp)
		}
	}
}

func TestTextIsServedAsPlainTextRegardlessOfExtension(t *testing.T) {
	b, _ := testBrowser(t)
	recorder := do(t, b, http.MethodGet, "/api/file?path=/notes.txt", nil)
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content type = %q", got)
	}
}

func TestAttachmentDispositionCanBeForced(t *testing.T) {
	b, _ := testBrowser(t)
	recorder := do(t, b, http.MethodGet, "/api/file?path=/photo.png&disposition=attachment", nil)
	if !strings.HasPrefix(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("disposition = %q", recorder.Header().Get("Content-Disposition"))
	}
}

// Range support is what makes video seek instead of downloading in full.
func TestRangeRequestsAreHonoured(t *testing.T) {
	b, _ := testBrowser(t)
	request := httptest.NewRequest(http.MethodGet, "/api/file?path=/clip.mp4", nil)
	request.Header.Set("Range", "bytes=10-19")
	recorder := httptest.NewRecorder()
	b.handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", recorder.Code)
	}
	if recorder.Body.Len() != 10 {
		t.Fatalf("body length = %d, want 10", recorder.Body.Len())
	}
	if got := recorder.Header().Get("Content-Range"); got != "bytes 10-19/4096" {
		t.Fatalf("Content-Range = %q", got)
	}
}

func TestUploadWritesIntoTheRequestedFolder(t *testing.T) {
	b, dir := testBrowser(t)
	body, contentType := multipartBody(t, "report.txt", "uploaded body")
	request := httptest.NewRequest(http.MethodPost, "/api/upload?path=/projects", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	b.handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "projects", "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "uploaded body" {
		t.Fatalf("stored body = %q", data)
	}
}

// A multipart filename is client-controlled and may carry a path.
func TestUploadFileNamesAreReducedToTheirBase(t *testing.T) {
	b, dir := testBrowser(t)
	body, contentType := multipartBody(t, "../../escaped.txt", "nope")
	request := httptest.NewRequest(http.MethodPost, "/api/upload?path=/", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	b.handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.txt")); err == nil {
		t.Fatal("upload escaped the workspace")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); err != nil {
		t.Fatalf("upload did not land in the workspace root: %v", err)
	}
}

func TestUploadIsSizeLimited(t *testing.T) {
	b, _ := testBrowser(t)
	b.config.maxUploadBytes = 64
	body, contentType := multipartBody(t, "big.bin", strings.Repeat("x", 4096))
	request := httptest.NewRequest(http.MethodPost, "/api/upload?path=/", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	b.handler().ServeHTTP(recorder, request)

	if recorder.Code == http.StatusCreated {
		t.Fatal("an oversized upload was accepted")
	}
}

func TestMkdirAndDelete(t *testing.T) {
	b, dir := testBrowser(t)
	if recorder := do(t, b, http.MethodPost, "/api/mkdir?path=/fresh", nil); recorder.Code != http.StatusCreated {
		t.Fatalf("mkdir status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh")); err != nil {
		t.Fatal(err)
	}
	if recorder := do(t, b, http.MethodPost, "/api/delete?path=/fresh", nil); recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh")); !os.IsNotExist(err) {
		t.Fatal("folder was not removed")
	}
	// The workspace root itself is never a target.
	if recorder := do(t, b, http.MethodPost, "/api/delete?path=/", nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("deleting the root returned %d, want 400", recorder.Code)
	}
}

func TestBasePathIsStrippedWhenConfigured(t *testing.T) {
	b, _ := testBrowser(t)
	b.config.basePath = "/apps/files"
	recorder := do(t, b, http.MethodGet, "/apps/files/api/list?path=/", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := do(t, b, http.MethodGet, "/apps/files/healthz", nil); recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d", recorder.Code)
	}
}

func TestHealthReportsOK(t *testing.T) {
	b, _ := testBrowser(t)
	recorder := do(t, b, http.MethodGet, "/healthz", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"ok"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestCleanPathNormalisation(t *testing.T) {
	for input, want := range map[string]string{
		"":           ".",
		"/":          ".",
		"/a/b":       "a/b",
		"a/b/":       "a/b",
		"/a//b":      "a/b",
		"/a/./b":     "a/b",
		"/a/c/../b":  "a/b",
		`\a\b`:       "a/b",
		"/../../a":   "a",
		"/a/../../b": "b",
		// Anchoring at "/" means surplus ".." are discarded rather than
		// climbing out, so a traversal attempt clamps to the workspace root.
		"/../a/../..": ".",
		"/../../..":   ".",
	} {
		got, err := cleanPath(input)
		if want == "" {
			if err == nil {
				t.Errorf("cleanPath(%q) = %q, want an error", input, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("cleanPath(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := cleanPath("a\x00b"); err == nil {
		t.Error("a null byte in a path was accepted")
	}
}

func multipartBody(t *testing.T, filename, content string) (io.Reader, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &buffer, writer.FormDataContentType()
}
