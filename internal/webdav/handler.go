package webdav

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xeonliu/TboxWebdav/internal/auth"
	"github.com/xeonliu/TboxWebdav/internal/config"
	"github.com/xeonliu/TboxWebdav/internal/tbox"
	"golang.org/x/net/webdav"
)

const chunkSize = 4 * 1024 * 1024 // 4 MB

// Handler is the main WebDAV HTTP handler.
type Handler struct {
	client     *tbox.Client
	credCache  *tbox.CredCache
	tokenCache *tbox.TokenCache
	quotaCache *tbox.QuotaCache
	lockSystem webdav.LockSystem
}

// NewHandler creates a new WebDAV Handler backed by the global caches.
func NewHandler(client *tbox.Client) *Handler {
	return &Handler{
		client:     client,
		credCache:  tbox.GlobalCredCache,
		tokenCache: tbox.GlobalTokenCache,
		quotaCache: tbox.GlobalQuotaCache,
		lockSystem: webdav.NewMemLS(),
	}
}

// statusRecorder wraps http.ResponseWriter to capture the status code written
// by the handler so it can be included in the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ServeHTTP dispatches incoming WebDAV requests to the appropriate method handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	switch r.Method {
	case "OPTIONS":
		h.handleOptions(rec, r)
	case "HEAD":
		h.handleHead(rec, r)
	case "GET":
		h.handleGet(rec, r)
	case "PUT":
		h.handlePut(rec, r)
	case "DELETE":
		h.handleDelete(rec, r)
	case "MKCOL":
		h.handleMkcol(rec, r)
	case "COPY":
		h.handleCopyMove(rec, r, false)
	case "MOVE":
		h.handleCopyMove(rec, r, true)
	case "PROPFIND":
		h.handlePropfind(rec, r)
	case "PROPPATCH":
		h.handleProppatch(rec, r)
	case "LOCK":
		h.handleLock(rec, r)
	case "UNLOCK":
		h.handleUnlock(rec, r)
	default:
		http.Error(rec, "Method Not Allowed", http.StatusMethodNotAllowed)
	}

	slog.Info("webdav",
		"method", r.Method,
		"path", requestPath(r),
		"status", rec.status,
		"duration", time.Since(start).Round(time.Millisecond),
	)
}

// --------------------------------------------------------------------------
// Credential resolution helpers
// --------------------------------------------------------------------------

// resolveUserToken returns the userToken for the current request, logging in
// via JAAuthCookie if only a cookie is available.
func (h *Handler) resolveUserToken(r *http.Request) (string, error) {
	ctx := r.Context()

	if token, ok := ctx.Value(auth.ContextKeyUserToken).(string); ok && token != "" {
		slog.Debug("auth: using UserToken from context")
		return token, nil
	}

	cookie, ok := ctx.Value(auth.ContextKeyJaCookie).(string)
	if !ok || cookie == "" {
		return "", fmt.Errorf("no credentials in context")
	}

	// Check token cache first.
	if cached := h.tokenCache.Get(cookie); cached != "" {
		slog.Debug("auth: userToken cache hit for JaCookie")
		return cached, nil
	}

	// Login using JAAuthCookie.
	slog.Debug("auth: JaCookie login attempt")
	loginRes, err := h.client.LoginUseJaccount(cookie)
	if err != nil {
		return "", fmt.Errorf("jaCookie login failed: %w", err)
	}
	slog.Info("auth: JaCookie login succeeded")
	h.tokenCache.Set(cookie, loginRes.UserToken)
	return loginRes.UserToken, nil
}

// resolveCred returns a valid SpaceCred for the given userToken, using the cache.
func (h *Handler) resolveCred(userToken string) (*tbox.SpaceCred, error) {
	if cached := h.credCache.Get(userToken); cached != nil {
		slog.Debug("auth: space cred cache hit")
		return cached, nil
	}
	slog.Debug("auth: space cred cache miss, fetching")
	cred, err := h.client.GetSpace(userToken)
	if err != nil {
		return nil, fmt.Errorf("GetSpace failed: %w", err)
	}
	h.credCache.Set(userToken, cred)
	return cred, nil
}

// accessMode extracts the AccessMode from the request context.
func accessMode(r *http.Request) config.AccessMode {
	if am, ok := r.Context().Value(auth.ContextKeyAccessMode).(config.AccessMode); ok {
		return am
	}
	return config.AccessModeFull
}

// requestPath returns the URL-decoded request path.
func requestPath(r *http.Request) string {
	p, err := url.PathUnescape(r.URL.Path)
	if err != nil {
		return r.URL.Path
	}
	if p == "" {
		return "/"
	}
	return p
}

// mimeType returns a MIME type for the given filename.
func mimeType(name string) string {
	if mt := mime.TypeByExtension(filepath.Ext(name)); mt != "" {
		return mt
	}
	return "application/octet-stream"
}

// --------------------------------------------------------------------------
// OPTIONS
// --------------------------------------------------------------------------

func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND, PROPPATCH, LOCK, UNLOCK")
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

// --------------------------------------------------------------------------
// HEAD / GET
// --------------------------------------------------------------------------

func (h *Handler) handleHead(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, true)
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, false)
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, headOnly bool) {
	userToken, err := h.resolveUserToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cred, err := h.resolveCred(userToken)
	if err != nil {
		http.Error(w, "Failed to get space credentials", http.StatusInternalServerError)
		return
	}

	path := requestPath(r)

	// Stat to confirm the file exists and to get its metadata.
	info, err := h.client.GetItemInfo(cred, path)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if info.Type != "file" {
		http.Error(w, "Is a directory", http.StatusMethodNotAllowed)
		return
	}

	fileSize, _ := strconv.ParseInt(info.Size, 10, 64)

	// Parse Range header.
	var rangeStart, rangeEnd int64 = -1, -1
	isPartial := false
	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		var s, e int64
		if n, _ := fmt.Sscanf(rangeHdr, "bytes=%d-%d", &s, &e); n >= 1 {
			rangeStart = s
			if n == 2 {
				rangeEnd = e
			}
			isPartial = true
		}
	}

	// Set response headers.
	w.Header().Set("Content-Type", mimeType(info.Name))
	w.Header().Set("ETag", fmt.Sprintf(`"%s"`, info.ETag))
	w.Header().Set("Last-Modified", info.ModificationTime.UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")

	if isPartial {
		end := rangeEnd
		if end < 0 {
			end = fileSize - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rangeStart, end, fileSize))
		w.Header().Set("Content-Length", strconv.FormatInt(end-rangeStart+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
	}

	if headOnly {
		return
	}

	body, _, err := h.client.GetFileStream(cred, path, rangeStart, rangeEnd)
	if err != nil {
		slog.Error("GET: GetFileStream failed", "path", path, "error", err)
		return
	}
	defer body.Close()
	if _, err := io.Copy(w, body); err != nil {
		slog.Warn("GET: stream copy interrupted", "path", path, "error", err)
	}
}

// --------------------------------------------------------------------------
// PUT
// --------------------------------------------------------------------------

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) {
	if accessMode(r) == config.AccessModeReadOnly {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	userToken, err := h.resolveUserToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cred, err := h.resolveCred(userToken)
	if err != nil {
		http.Error(w, "Failed to get space credentials", http.StatusInternalServerError)
		return
	}

	path := requestPath(r)
	contentLength := r.ContentLength

	slog.Info("PUT: upload started", "path", path, "size", contentLength)

	if contentLength >= 0 && contentLength <= chunkSize {
		// Small file: use simple upload.
		err = h.client.SimpleUpload(cred, path, r.Body, contentLength)
	} else {
		// Large file: use multipart upload.
		err = h.chunkedUpload(cred, path, r.Body, contentLength)
	}

	if err != nil {
		slog.Error("PUT: upload failed", "path", path, "error", err)
		http.Error(w, "Upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("PUT: upload completed", "path", path)
	w.WriteHeader(http.StatusCreated)
}

// chunkedUpload uploads a stream in 4 MB chunks using the multipart upload API.
// When contentLength is known (>= 0), it streams one chunk at a time using at most
// chunkSize bytes of memory. When contentLength is unknown (-1), it buffers the
// entire stream before uploading (acceptable since most PUT requests include
// Content-Length).
func (h *Handler) chunkedUpload(cred *tbox.SpaceCred, path string, body io.Reader, contentLength int64) error {
	if contentLength < 0 {
		// Unknown length: buffer everything, then recurse with known length.
		data, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		return h.chunkedUpload(cred, path, bytes.NewReader(data), int64(len(data)))
	}

	if contentLength == 0 {
		return h.client.SimpleUpload(cred, path, bytes.NewReader(nil), 0)
	}

	chunkCount := int((contentLength + chunkSize - 1) / chunkSize)
	if chunkCount == 1 {
		return h.client.SimpleUpload(cred, path, body, contentLength)
	}

	uploadInfo, err := h.client.StartChunkUpload(cred, path, chunkCount)
	if err != nil {
		return err
	}
	slog.Debug("PUT: multipart upload started", "path", path, "chunks", chunkCount, "confirmKey", uploadInfo.ConfirmKey)

	buf := make([]byte, chunkSize)
	for i := 1; i <= chunkCount; i++ {
		// Renew credentials if this part token is missing (batches cap at 50).
		if _, ok := uploadInfo.Parts[strconv.Itoa(i)]; !ok {
			remaining := make([]int, chunkCount-i+1)
			for j := range remaining {
				remaining[j] = i + j
			}
			slog.Debug("PUT: renewing chunk upload credentials", "path", path, "fromPart", i)
			uploadInfo, err = h.client.RenewChunkUpload(cred, uploadInfo.ConfirmKey, remaining)
			if err != nil {
				return err
			}
		}

		// Read exactly one chunk into the buffer (avoids holding all chunks in memory).
		thisChunkSize := int64(chunkSize)
		if i == chunkCount {
			thisChunkSize = contentLength - int64(i-1)*chunkSize
		}
		n, err := io.ReadFull(body, buf[:thisChunkSize])
		if err != nil && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("reading chunk %d: %w", i, err)
		}

		slog.Debug("PUT: uploading chunk", "path", path, "part", i, "of", chunkCount, "bytes", n)
		if err := h.client.UploadChunk(uploadInfo, bytes.NewReader(buf[:n]), i); err != nil {
			return fmt.Errorf("chunk %d upload failed: %w", i, err)
		}
	}

	_, err = h.client.ConfirmUpload(cred, uploadInfo.ConfirmKey)
	return err
}

// --------------------------------------------------------------------------
// DELETE
// --------------------------------------------------------------------------

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	am := accessMode(r)
	if am == config.AccessModeReadOnly || am == config.AccessModeNoDelete {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	userToken, err := h.resolveUserToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cred, err := h.resolveCred(userToken)
	if err != nil {
		http.Error(w, "Failed to get space credentials", http.StatusInternalServerError)
		return
	}

	path := requestPath(r)
	if err := h.client.DeleteItem(cred, path); err != nil {
		slog.Error("DELETE: failed", "path", path, "error", err)
		http.Error(w, "Delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("DELETE: succeeded", "path", path)
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------------------
// MKCOL
// --------------------------------------------------------------------------

func (h *Handler) handleMkcol(w http.ResponseWriter, r *http.Request) {
	if accessMode(r) == config.AccessModeReadOnly {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	userToken, err := h.resolveUserToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cred, err := h.resolveCred(userToken)
	if err != nil {
		http.Error(w, "Failed to get space credentials", http.StatusInternalServerError)
		return
	}

	path := requestPath(r)
	if err := h.client.CreateDirectory(cred, path); err != nil {
		slog.Error("MKCOL: failed", "path", path, "error", err)
		http.Error(w, "MKCOL failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("MKCOL: created", "path", path)
	w.WriteHeader(http.StatusCreated)
}

// --------------------------------------------------------------------------
// COPY / MOVE
// --------------------------------------------------------------------------

func (h *Handler) handleCopyMove(w http.ResponseWriter, r *http.Request, isMove bool) {
	if accessMode(r) == config.AccessModeReadOnly {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	userToken, err := h.resolveUserToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cred, err := h.resolveCred(userToken)
	if err != nil {
		http.Error(w, "Failed to get space credentials", http.StatusInternalServerError)
		return
	}

	src := requestPath(r)

	destHdr := r.Header.Get("Destination")
	if destHdr == "" {
		http.Error(w, "Destination header missing", http.StatusBadRequest)
		return
	}
	destURL, err := url.Parse(destHdr)
	if err != nil {
		http.Error(w, "Invalid Destination header", http.StatusBadRequest)
		return
	}
	dst, err := url.PathUnescape(destURL.Path)
	if err != nil {
		dst = destURL.Path
	}

	if err := h.client.CopyOrMoveItem(cred, src, dst, isMove); err != nil {
		op := "COPY"
		if isMove {
			op = "MOVE"
		}
		slog.Error(op+": failed", "src", src, "dst", dst, "error", err)
		http.Error(w, "Operation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// --------------------------------------------------------------------------
// PROPFIND
// --------------------------------------------------------------------------

func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) {
	userToken, err := h.resolveUserToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cred, err := h.resolveCred(userToken)
	if err != nil {
		http.Error(w, "Failed to get space credentials", http.StatusInternalServerError)
		return
	}

	path := requestPath(r)
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}

	var quota *tbox.SpaceQuotaInfo
	if path == "/" {
		quota = h.quotaCache.Get(userToken)
		if quota == nil {
			slog.Debug("PROPFIND: fetching quota info")
			q, err := h.client.GetSpaceQuotaInfo(userToken)
			if err != nil {
				slog.Warn("PROPFIND: quota fetch failed", "error", err)
			} else {
				h.quotaCache.Set(userToken, q)
				quota = q
			}
		}
	}

	type propfindEntry struct {
		path string
		item *tbox.MergedItemDto
		isRoot bool
	}

	var entries []propfindEntry

	if path == "/" {
		// Root is a virtual collection.
		entries = append(entries, propfindEntry{path: "/", isRoot: true})
	} else {
		info, err := h.client.GetItemInfo(cred, path)
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		entries = append(entries, propfindEntry{path: path, item: info})
	}

	// Depth 1: list children.
	if depth != "0" {
		var parentPath string
		var isCollection bool

		if path == "/" {
			parentPath = "/"
			isCollection = true
		} else if len(entries) > 0 && entries[0].item != nil && entries[0].item.Type == "dir" {
			parentPath = path
			isCollection = true
		}

		if isCollection {
			page := 1
			const pageSize = 100
			for {
				slog.Debug("PROPFIND: listing directory", "path", parentPath, "page", page)
				list, err := h.client.ListItems(cred, parentPath, page, pageSize)
				if err != nil {
					slog.Error("PROPFIND: ListItems failed", "path", parentPath, "page", page, "error", err)
					break
				}
				for i := range list.Contents {
					child := &list.Contents[i]
					childPath := parentPath
					if !strings.HasSuffix(childPath, "/") {
						childPath += "/"
					}
					childPath += child.Name
					entries = append(entries, propfindEntry{path: childPath, item: child})
				}
				if int64(page*pageSize) >= list.TotalNum {
					break
				}
				page++
			}
		}
	}

	// Render XML response.
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)

	if _, err := w.Write([]byte(xml.Header)); err != nil {
		slog.Error("PROPFIND: write error", "error", err)
		return
	}
	if _, err := w.Write([]byte(`<D:multistatus xmlns:D="DAV:">`)); err != nil {
		slog.Error("PROPFIND: write error", "error", err)
		return
	}
	for _, e := range entries {
		if _, err := w.Write(buildPropfindResponse(e.path, e.item, e.isRoot, quota)); err != nil {
			slog.Error("PROPFIND: write error", "error", err)
			return
		}
	}
	if _, err := w.Write([]byte(`</D:multistatus>`)); err != nil {
		slog.Error("PROPFIND: write error", "error", err)
	}
}

// buildPropfindResponse builds the <D:response> XML for one entry.
func buildPropfindResponse(path string, item *tbox.MergedItemDto, isRoot bool, quota *tbox.SpaceQuotaInfo) []byte {
	now := time.Now()

	var creationTime, modTime time.Time
	var etag, name, size, contentType string
	isDir := isRoot

	if item != nil {
		creationTime = item.CreationTime
		modTime = item.ModificationTime
		etag = item.ETag
		name = item.Name
		size = item.Size
		contentType = item.ContentType
		isDir = item.Type == "dir"
	} else if isRoot {
		creationTime = now
		modTime = now
		name = ""
	}

	if creationTime.IsZero() {
		creationTime = now
	}
	if modTime.IsZero() {
		modTime = now
	}

	// href must be URL-encoded but slashes preserved.
	href := encodePath(path)

	var sb strings.Builder
	sb.WriteString(`<D:response>`)
	sb.WriteString(`<D:href>`)
	sb.WriteString(xmlEscape(href))
	sb.WriteString(`</D:href>`)
	sb.WriteString(`<D:propstat><D:prop>`)
	sb.WriteString(fmt.Sprintf(`<D:creationdate>%s</D:creationdate>`, creationTime.UTC().Format("2006-01-02T15:04:05Z")))
	sb.WriteString(fmt.Sprintf(`<D:displayname>%s</D:displayname>`, xmlEscape(name)))
	sb.WriteString(fmt.Sprintf(`<D:getlastmodified>%s</D:getlastmodified>`, modTime.UTC().Format(http.TimeFormat)))
	if etag != "" {
		sb.WriteString(fmt.Sprintf(`<D:getetag>"%s"</D:getetag>`, xmlEscape(etag)))
	}

	if isDir {
		sb.WriteString(`<D:resourcetype><D:collection/></D:resourcetype>`)
		if isRoot && quota != nil {
			usedBytes := quota.Size
			availBytes := quota.AvailableSpace
			sb.WriteString(fmt.Sprintf(`<D:quota-used-bytes>%s</D:quota-used-bytes>`, xmlEscape(usedBytes)))
			sb.WriteString(fmt.Sprintf(`<D:quota-available-bytes>%s</D:quota-available-bytes>`, xmlEscape(availBytes)))
		}
	} else {
		sb.WriteString(`<D:resourcetype/>`)
		if size != "" {
			sb.WriteString(fmt.Sprintf(`<D:getcontentlength>%s</D:getcontentlength>`, xmlEscape(size)))
		}
		mt := contentType
		if mt == "" {
			mt = mimeType(name)
		}
		sb.WriteString(fmt.Sprintf(`<D:getcontenttype>%s</D:getcontenttype>`, xmlEscape(mt)))
	}

	sb.WriteString(`<D:supportedlock>`)
	sb.WriteString(`<D:lockentry><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockentry>`)
	sb.WriteString(`<D:lockentry><D:lockscope><D:shared/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockentry>`)
	sb.WriteString(`</D:supportedlock>`)

	sb.WriteString(`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`)
	sb.WriteString(`</D:response>`)
	return []byte(sb.String())
}

// encodePath URL-encodes each path segment, preserving slashes.
func encodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// xmlEscape escapes special XML characters.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// --------------------------------------------------------------------------
// PROPPATCH
// --------------------------------------------------------------------------

func (h *Handler) handleProppatch(w http.ResponseWriter, r *http.Request) {
	path := requestPath(r)
	href := encodePath(path)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<D:multistatus xmlns:D="DAV:">`+
		`<D:response><D:href>%s</D:href>`+
		`<D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`+
		`</D:response></D:multistatus>`, xmlEscape(href))
}

// --------------------------------------------------------------------------
// LOCK
// --------------------------------------------------------------------------

func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) {
	path := requestPath(r)
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "infinity"
	}

	// Parse the lock request body to find owner XML.
	body, _ := io.ReadAll(r.Body)
	ownerXML := extractOwner(body)

	duration := 30 * time.Minute
	if to := r.Header.Get("Timeout"); to != "" {
		if strings.HasPrefix(to, "Second-") {
			if sec, err := strconv.Atoi(strings.TrimPrefix(to, "Second-")); err == nil {
				duration = time.Duration(sec) * time.Second
			}
		}
	}

	details := webdav.LockDetails{
		Root:      path,
		Duration:  duration,
		OwnerXML:  ownerXML,
		ZeroDepth: depth == "0",
	}

	token, err := h.lockSystem.Create(time.Now(), details)
	if err != nil {
		if err == webdav.ErrLocked {
			http.Error(w, "Locked", http.StatusLocked)
			return
		}
		http.Error(w, "Lock failed", http.StatusInternalServerError)
		return
	}

	href := encodePath(path)
	timeoutStr := fmt.Sprintf("Second-%d", int(duration.Seconds()))

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Lock-Token", fmt.Sprintf("<%s>", token))
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<D:prop xmlns:D="DAV:">`+
		`<D:lockdiscovery>`+
		`<D:activelock>`+
		`<D:locktype><D:write/></D:locktype>`+
		`<D:lockscope><D:exclusive/></D:lockscope>`+
		`<D:depth>%s</D:depth>`+
		`<D:owner>%s</D:owner>`+
		`<D:timeout>%s</D:timeout>`+
		`<D:locktoken><D:href>%s</D:href></D:locktoken>`+
		`<D:lockroot><D:href>%s</D:href></D:lockroot>`+
		`</D:activelock>`+
		`</D:lockdiscovery>`+
		`</D:prop>`,
		depth, ownerXML, timeoutStr, xmlEscape(token), xmlEscape(href))
}

// extractOwner attempts to extract the <D:owner> element content from a lockinfo body.
func extractOwner(body []byte) string {
	type lockinfo struct {
		Owner struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"owner"`
	}
	var li lockinfo
	if err := xml.Unmarshal(body, &li); err == nil {
		return string(li.Owner.Inner)
	}
	return ""
}

// --------------------------------------------------------------------------
// UNLOCK
// --------------------------------------------------------------------------

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) {
	lockToken := r.Header.Get("Lock-Token")
	lockToken = strings.Trim(lockToken, "<>")
	if lockToken != "" {
		h.lockSystem.Unlock(time.Now(), lockToken)
	}
	w.WriteHeader(http.StatusNoContent)
}
