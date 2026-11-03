package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const notBuiltPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Cistern</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem; line-height: 1.5">
<h1>Cistern Admin UI was not built</h1>
<p>This binary was built without the web UI. The API under <code>/api/v1</code> works.</p>
<p>Build the UI with <code>make ui build-ui</code> (Node.js 22, then <code>go build -tags ui</code>), or use the container image.</p>
</body>
</html>
`

var (
	scriptRe = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	srcRe    = regexp.MustCompile(`(?i)\bsrc\s*=`)
	hashedRe = regexp.MustCompile(`[.-][A-Za-z0-9_-]{8,}\.(js|mjs|css|woff2?|ttf|png|jpe?g|gif|svg|webp|avif|ico|map|wasm)$`)
)

func inlineScriptHashes(html []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range scriptRe.FindAllSubmatch(html, -1) {
		if srcRe.Match(m[1]) {
			continue
		}
		sum := sha256.Sum256(m[2])
		h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

func buildCSP(scriptHashes []string) string {
	script := "script-src 'self'"
	if len(scriptHashes) > 0 {
		script += " " + strings.Join(scriptHashes, " ")
	}
	return strings.Join([]string{
		"default-src 'self'", script, "style-src 'self' 'unsafe-inline'", "img-src 'self' data:", "font-src 'self' data:",
		"connect-src 'self'", "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'",
	}, "; ")
}

const apiCSP = "default-src 'none'; frame-ancestors 'none'"

type uiHandler struct {
	files   fs.FS
	shell   []byte
	csp     string
	headers func(http.Header, string)
	modTime time.Time
}

func newUIHandler(files fs.FS, headers func(http.Header, string)) (*uiHandler, error) {
	h := &uiHandler{files: files, headers: headers, modTime: time.Now()}
	if files != nil {
		shell, err := fs.ReadFile(files, "_shell.html")
		if err != nil {
			return nil, err
		}
		h.shell = shell
	} else {
		h.shell = []byte(notBuiltPage)
	}
	h.csp = buildCSP(inlineScriptHashes(h.shell))
	return h, nil
}

func (h *uiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.headers(w.Header(), h.csp)
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if h.files != nil && name != "" && name != "_shell.html" && fs.ValidPath(name) {
		if f, err := h.files.Open(name); err == nil {
			st, serr := f.Stat()
			if serr == nil && !st.IsDir() {
				h.serveFile(w, r, name, f, st.ModTime())
				f.Close()
				return
			}
			f.Close()
		}
	}
	if path.Ext(name) != "" && strings.HasPrefix(name, "assets/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", h.modTime, bytes.NewReader(h.shell))
}

func (h *uiHandler) serveFile(w http.ResponseWriter, r *http.Request, name string, f fs.File, mod time.Time) {
	if strings.HasPrefix(name, "assets/") || hashedRe.MatchString(name) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, mod, rs)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, mod, bytes.NewReader(data))
}
