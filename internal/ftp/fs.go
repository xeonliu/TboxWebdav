package ftp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
	"github.com/xeonliu/TboxWebdav/internal/config"
	"github.com/xeonliu/TboxWebdav/internal/tbox"
)

// errNotSupported is returned for afero.Fs methods that are not required for FTP.
var errNotSupported = errors.New("operation not supported")

// TboxFs implements afero.Fs (ftpserver.ClientDriver) for the Tbox API.
// It also implements ClientDriverExtensionFileList and
// ClientDriverExtentionFileTransfer to simplify the required afero.File methods.
type TboxFs struct {
	client   *tbox.Client
	cred     *tbox.SpaceCred
	dirCache *tbox.DirCache
	access   config.AccessMode
}

// NewTboxFs creates a new TboxFs for a given set of credentials.
func NewTboxFs(client *tbox.Client, cred *tbox.SpaceCred, dirCache *tbox.DirCache, access config.AccessMode) *TboxFs {
	return &TboxFs{
		client:   client,
		cred:     cred,
		dirCache: dirCache,
		access:   access,
	}
}

// --------------------------------------------------------------------------
// ClientDriverExtensionFileList – preferred directory listing
// --------------------------------------------------------------------------

// ReadDir lists the contents of name and returns os.FileInfo slice.
// ftpserverlib uses this instead of Open+Readdir when the extension is present.
func (fs *TboxFs) ReadDir(name string) ([]os.FileInfo, error) {
	name = cleanPath(name)

	const pageSize = 100
	var infos []os.FileInfo
	for page := 1; ; page++ {
		list, err := fs.listItems(name, page, pageSize)
		if err != nil {
			return nil, fmt.Errorf("ReadDir %s: %w", name, err)
		}
		for i := range list.Contents {
			infos = append(infos, itemToFileInfo(&list.Contents[i]))
		}
		if int64(page*pageSize) >= list.TotalNum {
			break
		}
	}
	return infos, nil
}

// --------------------------------------------------------------------------
// ClientDriverExtentionFileTransfer – file upload/download
// --------------------------------------------------------------------------

// GetHandle returns a FileTransfer handle for reading (RETR) or writing (STOR/APPE).
// flags uses os.O_RDONLY / os.O_WRONLY / os.O_RDWR / os.O_APPEND / os.O_CREATE.
func (fs *TboxFs) GetHandle(name string, flags int, offset int64) (ftpserver.FileTransfer, error) {
	name = cleanPath(name)

	writing := flags&(os.O_WRONLY|os.O_RDWR) != 0
	if writing {
		if fs.access == config.AccessModeReadOnly {
			return nil, fmt.Errorf("read-only access")
		}
		return newWriteHandle(fs.client, fs.cred, fs.dirCache, name), nil
	}

	// Download.
	body, _, err := fs.client.GetFileStream(fs.cred, name, offset, -1)
	if err != nil {
		return nil, fmt.Errorf("GetHandle read %s: %w", name, err)
	}
	return newReadHandle(body), nil
}

// --------------------------------------------------------------------------
// afero.Fs implementation
// --------------------------------------------------------------------------

// Name returns the filesystem name.
func (fs *TboxFs) Name() string { return "TboxFs" }

// Stat returns os.FileInfo for name.
func (fs *TboxFs) Stat(name string) (os.FileInfo, error) {
	name = cleanPath(name)
	if name == "/" {
		return rootFileInfo(), nil
	}

	if cached := fs.dirCache.GetItemInfo(name); cached != nil {
		return itemToFileInfo(cached), nil
	}
	info, err := fs.client.GetItemInfo(fs.cred, name)
	if err != nil {
		return nil, &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
	}
	fs.dirCache.SetItemInfo(name, info)
	return itemToFileInfo(info), nil
}

// Mkdir creates a directory at name.
func (fs *TboxFs) Mkdir(name string, _ os.FileMode) error {
	if fs.access == config.AccessModeReadOnly {
		return fmt.Errorf("read-only access")
	}
	name = cleanPath(name)
	if err := fs.client.CreateDirectory(fs.cred, name); err != nil {
		return fmt.Errorf("Mkdir %s: %w", name, err)
	}
	fs.dirCache.Invalidate(name)
	return nil
}

// MkdirAll creates name and all parents.
func (fs *TboxFs) MkdirAll(name string, perm os.FileMode) error {
	return fs.Mkdir(name, perm)
}

// Remove deletes a file or directory.
func (fs *TboxFs) Remove(name string) error {
	if fs.access == config.AccessModeReadOnly || fs.access == config.AccessModeNoDelete {
		return fmt.Errorf("delete not permitted")
	}
	name = cleanPath(name)
	if err := fs.client.DeleteItem(fs.cred, name); err != nil {
		return fmt.Errorf("Remove %s: %w", name, err)
	}
	fs.dirCache.Invalidate(name)
	return nil
}

// RemoveAll deletes name recursively; mapped to Remove for cloud storage.
func (fs *TboxFs) RemoveAll(name string) error {
	return fs.Remove(name)
}

// Rename moves src to dst.
func (fs *TboxFs) Rename(oldname, newname string) error {
	if fs.access == config.AccessModeReadOnly {
		return fmt.Errorf("read-only access")
	}
	oldname = cleanPath(oldname)
	newname = cleanPath(newname)
	if err := fs.client.CopyOrMoveItem(fs.cred, oldname, newname, true); err != nil {
		return fmt.Errorf("Rename %s -> %s: %w", oldname, newname, err)
	}
	fs.dirCache.Invalidate(oldname)
	fs.dirCache.Invalidate(newname)
	return nil
}

// Chmod is a no-op (cloud storage has no Unix permissions).
func (fs *TboxFs) Chmod(_ string, _ os.FileMode) error { return nil }

// Chown is a no-op.
func (fs *TboxFs) Chown(_ string, _, _ int) error { return nil }

// Chtimes is a no-op.
func (fs *TboxFs) Chtimes(_ string, _, _ time.Time) error { return nil }

// Create opens name for writing (delegates to GetHandle).
func (fs *TboxFs) Create(name string) (afero.File, error) {
	h, err := fs.GetHandle(name, os.O_WRONLY|os.O_CREATE, 0)
	if err != nil {
		return nil, err
	}
	return &fileAdapter{FileTransfer: h, name: name}, nil
}

// Open opens name for reading.
func (fs *TboxFs) Open(name string) (afero.File, error) {
	return fs.OpenFile(name, os.O_RDONLY, 0)
}

// OpenFile opens name with the given flags.
func (fs *TboxFs) OpenFile(name string, flag int, _ os.FileMode) (afero.File, error) {
	h, err := fs.GetHandle(name, flag, 0)
	if err != nil {
		return nil, err
	}
	return &fileAdapter{FileTransfer: h, name: name}, nil
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// listItems retrieves one page of a directory listing, using the DirCache.
func (fs *TboxFs) listItems(p string, page, pageSize int) (*tbox.ItemListDto, error) {
	if cached := fs.dirCache.GetDirList(p, page, pageSize); cached != nil {
		return cached, nil
	}
	list, err := fs.client.ListItems(fs.cred, p, page, pageSize)
	if err != nil {
		return nil, err
	}
	fs.dirCache.SetDirList(p, page, pageSize, list)
	return list, nil
}

// cleanPath normalises an FTP path to an absolute slash-delimited path.
func cleanPath(p string) string {
	if p == "" || p == "." {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// Collapse consecutive slashes in a single pass.
	var b strings.Builder
	b.Grow(len(p))
	prevSlash := false
	for _, c := range p {
		if c == '/' {
			if !prevSlash {
				b.WriteRune(c)
			}
			prevSlash = true
		} else {
			b.WriteRune(c)
			prevSlash = false
		}
	}
	p = b.String()
	// Remove trailing slash except for root.
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}

// --------------------------------------------------------------------------
// TboxFileInfo
// --------------------------------------------------------------------------

// tboxFileInfo implements os.FileInfo for a Tbox item.
type tboxFileInfo struct {
	name    string
	size    int64
	modTime time.Time
	isDir   bool
}

func (fi *tboxFileInfo) Name() string      { return fi.name }
func (fi *tboxFileInfo) Size() int64       { return fi.size }
func (fi *tboxFileInfo) Mode() os.FileMode {
	if fi.isDir {
		return os.ModeDir | 0755
	}
	return 0644
}
func (fi *tboxFileInfo) ModTime() time.Time { return fi.modTime }
func (fi *tboxFileInfo) IsDir() bool        { return fi.isDir }
func (fi *tboxFileInfo) Sys() interface{}   { return nil }

// itemToFileInfo converts a MergedItemDto to os.FileInfo.
func itemToFileInfo(item *tbox.MergedItemDto) os.FileInfo {
	var size int64
	if item.Size != "" {
		size, _ = strconv.ParseInt(item.Size, 10, 64)
	}
	modTime := item.ModificationTime
	if modTime.IsZero() {
		modTime = item.CreationTime
	}
	if modTime.IsZero() {
		modTime = time.Now()
	}
	return &tboxFileInfo{
		name:    item.Name,
		size:    size,
		modTime: modTime,
		isDir:   item.Type == "dir",
	}
}

// rootFileInfo returns an os.FileInfo for the virtual root directory.
func rootFileInfo() os.FileInfo {
	return &tboxFileInfo{
		name:    "",
		size:    0,
		modTime: time.Now(),
		isDir:   true,
	}
}

// --------------------------------------------------------------------------
// readHandle – FileTransfer for downloads
// --------------------------------------------------------------------------

// readHandle wraps an HTTP response body as a FileTransfer (read-only).
// It buffers the stream into memory on first Seek call to support seeking.
type readHandle struct {
	body io.ReadCloser
	buf  *bytes.Reader // populated lazily on first Seek
}

func newReadHandle(body io.ReadCloser) *readHandle {
	return &readHandle{body: body}
}

func (h *readHandle) ensureBuffered() error {
	if h.buf != nil {
		return nil
	}
	data, err := io.ReadAll(h.body)
	h.body.Close()
	if err != nil {
		return err
	}
	h.buf = bytes.NewReader(data)
	return nil
}

func (h *readHandle) Read(p []byte) (int, error) {
	if h.buf != nil {
		return h.buf.Read(p)
	}
	// Stream directly from the body without buffering for sequential reads.
	return h.body.Read(p)
}

func (h *readHandle) Write(_ []byte) (int, error) {
	return 0, errNotSupported
}

func (h *readHandle) Seek(offset int64, whence int) (int64, error) {
	if err := h.ensureBuffered(); err != nil {
		return 0, err
	}
	return h.buf.Seek(offset, whence)
}

func (h *readHandle) Close() error {
	if h.buf != nil {
		return nil // body already closed in ensureBuffered
	}
	return h.body.Close()
}

// --------------------------------------------------------------------------
// writeHandle – FileTransfer for uploads
// --------------------------------------------------------------------------

// writeHandle buffers all written bytes and uploads them to Tbox on Close.
type writeHandle struct {
	client   *tbox.Client
	cred     *tbox.SpaceCred
	dirCache *tbox.DirCache
	name     string
	buf      bytes.Buffer
	closed   bool
}

const ftpChunkSize = 4 * 1024 * 1024 // 4 MB

func newWriteHandle(client *tbox.Client, cred *tbox.SpaceCred, dirCache *tbox.DirCache, name string) *writeHandle {
	return &writeHandle{client: client, cred: cred, dirCache: dirCache, name: name}
}

func (h *writeHandle) Read(_ []byte) (int, error) {
	return 0, errNotSupported
}

func (h *writeHandle) Write(p []byte) (int, error) {
	return h.buf.Write(p)
}

func (h *writeHandle) Seek(_ int64, _ int) (int64, error) {
	return 0, errNotSupported
}

func (h *writeHandle) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true

	data := h.buf.Bytes()
	size := int64(len(data))

	var uploadErr error
	if size <= ftpChunkSize {
		uploadErr = h.client.SimpleUpload(h.cred, h.name, bytes.NewReader(data), size)
	} else {
		uploadErr = chunkedUpload(h.client, h.cred, h.name, bytes.NewReader(data), size)
	}
	if uploadErr != nil {
		return fmt.Errorf("upload %s: %w", h.name, uploadErr)
	}
	h.dirCache.Invalidate(h.name)
	return nil
}

// chunkedUpload uploads data in ftpChunkSize chunks using the multipart upload API.
func chunkedUpload(client *tbox.Client, cred *tbox.SpaceCred, path string, data io.ReadSeeker, size int64) error {
	chunkCount := int((size + ftpChunkSize - 1) / ftpChunkSize)
	uploadInfo, err := client.StartChunkUpload(cred, path, chunkCount)
	if err != nil {
		return err
	}

	buf := make([]byte, ftpChunkSize)
	for i := 1; i <= chunkCount; i++ {
		if _, ok := uploadInfo.Parts[strconv.Itoa(i)]; !ok {
			remaining := make([]int, chunkCount-i+1)
			for j := range remaining {
				remaining[j] = i + j
			}
			uploadInfo, err = client.RenewChunkUpload(cred, uploadInfo.ConfirmKey, remaining)
			if err != nil {
				return err
			}
		}

		thisChunkSize := int64(ftpChunkSize)
		if i == chunkCount {
			thisChunkSize = size - int64(i-1)*ftpChunkSize
		}
		n, readErr := io.ReadFull(data, buf[:thisChunkSize])
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return fmt.Errorf("reading chunk %d: %w", i, readErr)
		}
		if err := client.UploadChunk(uploadInfo, bytes.NewReader(buf[:n]), i); err != nil {
			return fmt.Errorf("chunk %d upload failed: %w", i, err)
		}
	}

	_, err = client.ConfirmUpload(cred, uploadInfo.ConfirmKey)
	return err
}

// --------------------------------------------------------------------------
// fileAdapter – adapts a FileTransfer to afero.File
// --------------------------------------------------------------------------

// fileAdapter wraps a FileTransfer (read or write handle) to satisfy afero.File.
// Only Read, Write, Seek, Close and Name are usable; all other methods return errors.
type fileAdapter struct {
	ftpserver.FileTransfer
	name string
}

func (f *fileAdapter) Name() string                           { return f.name }
func (f *fileAdapter) ReadAt(_ []byte, _ int64) (int, error)    { return 0, errNotSupported }
func (f *fileAdapter) WriteAt(_ []byte, _ int64) (int, error)   { return 0, errNotSupported }
func (f *fileAdapter) Stat() (os.FileInfo, error)               { return nil, errNotSupported }
func (f *fileAdapter) Sync() error                              { return nil }
func (f *fileAdapter) Truncate(_ int64) error                   { return errNotSupported }
func (f *fileAdapter) WriteString(s string) (int, error)        { return f.Write([]byte(s)) }
func (f *fileAdapter) Readdir(_ int) ([]os.FileInfo, error)     { return nil, errNotSupported }
func (f *fileAdapter) Readdirnames(_ int) ([]string, error)     { return nil, errNotSupported }
