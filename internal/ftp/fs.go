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

	// Download: open with a Range request starting at offset.
	body, _, err := fs.client.GetFileStream(fs.cred, name, offset, -1)
	if err != nil {
		return nil, fmt.Errorf("GetHandle read %s: %w", name, err)
	}
	return newReadHandle(fs.client, fs.cred, name, body, offset), nil
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

// readHandle is a FileTransfer for downloading a file.
// It tracks the current read position and re-issues an HTTP Range request on
// Seek, so no part of the file is ever fully buffered in memory.
type readHandle struct {
	client *tbox.Client
	cred   *tbox.SpaceCred
	name   string
	body   io.ReadCloser
	pos    int64
}

func newReadHandle(client *tbox.Client, cred *tbox.SpaceCred, name string, body io.ReadCloser, pos int64) *readHandle {
	return &readHandle{client: client, cred: cred, name: name, body: body, pos: pos}
}

func (h *readHandle) Read(p []byte) (int, error) {
	n, err := h.body.Read(p)
	h.pos += int64(n)
	return n, err
}

func (h *readHandle) Write(_ []byte) (int, error) { return 0, errNotSupported }

func (h *readHandle) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = h.pos + offset
	case io.SeekEnd:
		return 0, fmt.Errorf("seek from end not supported")
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if newPos < 0 {
		return 0, fmt.Errorf("negative seek position: %d", newPos)
	}
	if newPos == h.pos {
		return h.pos, nil
	}
	// Re-open the stream at the new position via an HTTP Range request.
	h.body.Close()
	body, _, err := h.client.GetFileStream(h.cred, h.name, newPos, -1)
	if err != nil {
		return 0, fmt.Errorf("seek to %d: %w", newPos, err)
	}
	h.body = body
	h.pos = newPos
	return newPos, nil
}

func (h *readHandle) Close() error { return h.body.Close() }

// --------------------------------------------------------------------------
// writeHandle – FileTransfer for uploads
// --------------------------------------------------------------------------

// writeHandle is a FileTransfer for uploading a file.
// Data is piped through an io.Pipe to a background goroutine that uploads it
// to Tbox in 4 MB chunks – no full-file buffering in memory.
type writeHandle struct {
	pw     *io.PipeWriter
	done   chan error
	closed bool
}

const ftpChunkSize = 4 * 1024 * 1024 // 4 MB chunk size for multipart uploads
const ftpPartBatch  = 50              // presigned URL batch size per API call

func newWriteHandle(client *tbox.Client, cred *tbox.SpaceCred, dirCache *tbox.DirCache, name string) *writeHandle {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := streamUpload(client, cred, dirCache, name, pr)
		if err != nil {
			pr.CloseWithError(err) // propagates error to any pending Write
		} else {
			pr.Close()
		}
		done <- err
	}()
	return &writeHandle{pw: pw, done: done}
}

func (h *writeHandle) Read(_ []byte) (int, error)        { return 0, errNotSupported }
func (h *writeHandle) Write(p []byte) (int, error)        { return h.pw.Write(p) }
func (h *writeHandle) Seek(_ int64, _ int) (int64, error) { return 0, errNotSupported }

func (h *writeHandle) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	h.pw.Close() // signals EOF to the upload goroutine
	return <-h.done
}

// TransferError implements ftpserver.FileTransferError.
// Called by ftpserverlib when a data-connection error occurs so we can abort
// the background upload instead of waiting for a normal Close.
func (h *writeHandle) TransferError(err error) {
	if h.closed {
		return
	}
	h.closed = true
	h.pw.CloseWithError(err)
	// Non-blocking drain: the done channel is buffered (size 1); if the
	// goroutine has already finished it will have sent, so we collect it now.
	// If it hasn't, the send will land in the buffer and be GC'd with the handle.
	select {
	case <-h.done:
	default:
	}
}

// isReadEOF returns true when err signals end-of-stream from io.ReadFull.
func isReadEOF(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// streamUpload reads from r in 4 MB chunks and uploads to Tbox.
// Files that fit in one chunk use SimpleUpload; larger files use the multipart
// upload API, requesting presigned URLs in batches of ftpPartBatch.
func streamUpload(client *tbox.Client, cred *tbox.SpaceCred, dirCache *tbox.DirCache, name string, r io.Reader) error {
	buf := make([]byte, ftpChunkSize)

	// Read the first chunk.
	n, err := io.ReadFull(r, buf)
	if err != nil && !isReadEOF(err) {
		return err
	}

	if isReadEOF(err) {
		// Entire file fits in one chunk; use simple upload.
		uploadErr := client.SimpleUpload(cred, name, bytes.NewReader(buf[:n]), int64(n))
		if uploadErr == nil {
			dirCache.Invalidate(name)
		}
		return uploadErr
	}

	// Multi-chunk upload. Request the first batch of presigned part URLs.
	uploadInfo, err := client.StartChunkUpload(cred, name, ftpPartBatch)
	if err != nil {
		return err
	}

	partNum := 1
	for {
		// Ensure we have a presigned URL for this part number.
		if _, ok := uploadInfo.Parts[strconv.Itoa(partNum)]; !ok {
			batch := make([]int, ftpPartBatch)
			for i := range batch {
				batch[i] = partNum + i
			}
			uploadInfo, err = client.RenewChunkUpload(cred, uploadInfo.ConfirmKey, batch)
			if err != nil {
				return err
			}
		}

		if uploadErr := client.UploadChunk(uploadInfo, bytes.NewReader(buf[:n]), partNum); uploadErr != nil {
			return fmt.Errorf("chunk %d upload failed: %w", partNum, uploadErr)
		}

		// Read the next chunk.
		n, err = io.ReadFull(r, buf)
		if err != nil && !isReadEOF(err) {
			return fmt.Errorf("reading after chunk %d: %w", partNum, err)
		}

		if isReadEOF(err) {
			if n > 0 {
				// Upload the final partial chunk.
				partNum++
				if _, ok := uploadInfo.Parts[strconv.Itoa(partNum)]; !ok {
					uploadInfo, err = client.RenewChunkUpload(cred, uploadInfo.ConfirmKey, []int{partNum})
					if err != nil {
						return err
					}
				}
				if uploadErr := client.UploadChunk(uploadInfo, bytes.NewReader(buf[:n]), partNum); uploadErr != nil {
					return fmt.Errorf("final chunk upload failed: %w", uploadErr)
				}
			}
			break
		}
		partNum++
	}

	_, err = client.ConfirmUpload(cred, uploadInfo.ConfirmKey)
	if err == nil {
		dirCache.Invalidate(name)
	}
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
