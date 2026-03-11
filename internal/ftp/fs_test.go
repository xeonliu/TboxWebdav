package ftp

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xeonliu/TboxWebdav/internal/tbox"
)

// --------------------------------------------------------------------------
// cleanPath
// --------------------------------------------------------------------------

func TestCleanPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{".", "/"},
		{"/", "/"},
		{"foo", "/foo"},
		{"/foo", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"/foo/", "/foo"},
		{"//foo//bar//", "/foo/bar"},
		{"///", "/"},
		{"/a//b///c", "/a/b/c"},
	}
	for _, c := range cases {
		got := cleanPath(c.in)
		if got != c.want {
			t.Errorf("cleanPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --------------------------------------------------------------------------
// isReadEOF
// --------------------------------------------------------------------------

func TestIsReadEOF(t *testing.T) {
	if !isReadEOF(io.EOF) {
		t.Error("isReadEOF(io.EOF) should be true")
	}
	if !isReadEOF(io.ErrUnexpectedEOF) {
		t.Error("isReadEOF(io.ErrUnexpectedEOF) should be true")
	}
	if isReadEOF(nil) {
		t.Error("isReadEOF(nil) should be false")
	}
	if isReadEOF(io.ErrClosedPipe) {
		t.Error("isReadEOF(io.ErrClosedPipe) should be false")
	}
	if isReadEOF(errors.New("some error")) {
		t.Error("isReadEOF(arbitrary error) should be false")
	}
}

// --------------------------------------------------------------------------
// readHandle – position tracking and seek
// --------------------------------------------------------------------------

func TestReadHandleReadAdvancesPos(t *testing.T) {
	content := "hello world"
	h := &readHandle{
		body: io.NopCloser(strings.NewReader(content)),
		pos:  0,
	}

	buf := make([]byte, 5)
	n, err := h.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("read content: got %q, want %q", string(buf[:n]), "hello")
	}
	if h.pos != 5 {
		t.Errorf("pos after read: got %d, want 5", h.pos)
	}

	// second read
	n2, err2 := h.Read(buf)
	if err2 != nil {
		t.Fatalf("unexpected error on second read: %v", err2)
	}
	if string(buf[:n2]) != " worl" {
		t.Errorf("second read: got %q, want %q", string(buf[:n2]), " worl")
	}
	if h.pos != 10 {
		t.Errorf("pos after second read: got %d, want 10", h.pos)
	}
}

// TestReadHandleSeekSamePosition verifies that seeking to the current position
// is a no-op (no new HTTP request) – this is critical for the REST+RETR flow
// where ftpserverlib calls GetHandle(offset) and then immediately Seek(offset, 0).
func TestReadHandleSeekSamePosition(t *testing.T) {
	originalBody := io.NopCloser(strings.NewReader("data"))
	h := &readHandle{
		body: originalBody,
		pos:  42,
	}

	pos, err := h.Seek(42, io.SeekStart)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pos != 42 {
		t.Errorf("returned position: got %d, want 42", pos)
	}
	// Body must not have been replaced (no HTTP request made).
	if h.body != originalBody {
		t.Error("body was replaced on no-op seek")
	}
}

func TestReadHandleSeekCurrentSamePosition(t *testing.T) {
	originalBody := io.NopCloser(strings.NewReader("data"))
	h := &readHandle{
		body: originalBody,
		pos:  10,
	}
	// SeekCurrent with offset 0 should also be a no-op.
	pos, err := h.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pos != 10 {
		t.Errorf("returned position: got %d, want 10", pos)
	}
	if h.body != originalBody {
		t.Error("body was replaced on no-op seek")
	}
}

func TestReadHandleSeekNegativePosition(t *testing.T) {
	h := &readHandle{
		body: io.NopCloser(strings.NewReader("data")),
		pos:  5,
	}
	_, err := h.Seek(-10, io.SeekStart)
	if err == nil {
		t.Error("expected error for negative seek position")
	}
}

func TestReadHandleSeekFromEnd(t *testing.T) {
	h := &readHandle{
		body: io.NopCloser(strings.NewReader("data")),
		pos:  0,
	}
	_, err := h.Seek(0, io.SeekEnd)
	if err == nil {
		t.Error("expected error for SeekEnd (not supported)")
	}
}

func TestReadHandleSeekInvalidWhence(t *testing.T) {
	h := &readHandle{
		body: io.NopCloser(strings.NewReader("data")),
		pos:  0,
	}
	_, err := h.Seek(0, 99)
	if err == nil {
		t.Error("expected error for invalid whence")
	}
}

func TestReadHandleWriteNotSupported(t *testing.T) {
	h := &readHandle{body: io.NopCloser(strings.NewReader(""))}
	_, err := h.Write([]byte("x"))
	if !errors.Is(err, errNotSupported) {
		t.Errorf("Write should return errNotSupported, got %v", err)
	}
}

func TestReadHandleClose(t *testing.T) {
	h := &readHandle{
		body: io.NopCloser(strings.NewReader("data")),
	}
	if err := h.Close(); err != nil {
		t.Errorf("unexpected error from Close: %v", err)
	}
}

// --------------------------------------------------------------------------
// writeHandle – pipe mechanics
// --------------------------------------------------------------------------

// TestWriteHandleSeekNotSupported confirms that Seek on a writeHandle returns
// errNotSupported, so ftpserverlib correctly rejects REST+STOR.
func TestWriteHandleSeekNotSupported(t *testing.T) {
	h := &writeHandle{done: make(chan error, 1)}
	_, err := h.Seek(0, io.SeekStart)
	if !errors.Is(err, errNotSupported) {
		t.Errorf("Seek should return errNotSupported, got %v", err)
	}
}

func TestWriteHandleReadNotSupported(t *testing.T) {
	h := &writeHandle{done: make(chan error, 1)}
	_, err := h.Read(make([]byte, 1))
	if !errors.Is(err, errNotSupported) {
		t.Errorf("Read should return errNotSupported, got %v", err)
	}
}

// TestWriteHandleCloseTwice verifies that double-Close is safe.
func TestWriteHandleCloseTwice(t *testing.T) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(io.Discard, pr)
		// pr is closed by pw.Close(); io.Copy may return nil or io.ErrClosedPipe –
		// either is acceptable here.
		if copyErr != nil && !errors.Is(copyErr, io.ErrClosedPipe) {
			t.Errorf("unexpected error in discard goroutine: %v", copyErr)
		}
		done <- nil
	}()
	h := &writeHandle{pw: pw, done: done}
	if err := h.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got: %v", err)
	}
}

// TestWriteHandleTransferError verifies that TransferError closes the pipe
// and does not block.
func TestWriteHandleTransferError(t *testing.T) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, pr)
		done <- err
	}()
	h := &writeHandle{pw: pw, done: done}
	transferErr := errors.New("connection lost")
	h.TransferError(transferErr)
	// Verify closed flag is set.
	if !h.closed {
		t.Error("closed flag should be set after TransferError")
	}
	// Drain the goroutine and verify it received the pipe error.
	goroutineErr := <-done
	if goroutineErr == nil {
		t.Error("goroutine should have received a non-nil error after TransferError")
	}
}

// --------------------------------------------------------------------------
// tboxFileInfo
// --------------------------------------------------------------------------

func TestTboxFileInfoFile(t *testing.T) {
	now := time.Now()
	fi := &tboxFileInfo{name: "report.pdf", size: 4096, modTime: now, isDir: false}

	if fi.Name() != "report.pdf" {
		t.Errorf("Name: got %q", fi.Name())
	}
	if fi.Size() != 4096 {
		t.Errorf("Size: got %d", fi.Size())
	}
	if fi.IsDir() {
		t.Error("IsDir should be false")
	}
	if fi.Mode() != 0644 {
		t.Errorf("Mode: got %v, want 0644", fi.Mode())
	}
	if fi.ModTime() != now {
		t.Errorf("ModTime mismatch")
	}
}

func TestTboxFileInfoDir(t *testing.T) {
	fi := &tboxFileInfo{name: "docs", isDir: true}
	if !fi.IsDir() {
		t.Error("IsDir should be true")
	}
	if fi.Mode()&os.ModeDir == 0 {
		t.Errorf("Mode should include ModeDir, got %v", fi.Mode())
	}
}

// --------------------------------------------------------------------------
// itemToFileInfo
// --------------------------------------------------------------------------

func TestItemToFileInfoFile(t *testing.T) {
	now := time.Now()
	item := &tbox.MergedItemDto{
		Name:             "video.mp4",
		Size:             "10240",
		Type:             "file",
		ModificationTime: now,
	}
	fi := itemToFileInfo(item)
	if fi.Name() != "video.mp4" {
		t.Errorf("Name: got %q", fi.Name())
	}
	if fi.Size() != 10240 {
		t.Errorf("Size: got %d", fi.Size())
	}
	if fi.IsDir() {
		t.Error("IsDir should be false for type=file")
	}
}

func TestItemToFileInfoDir(t *testing.T) {
	item := &tbox.MergedItemDto{
		Name: "photos",
		Type: "dir",
	}
	fi := itemToFileInfo(item)
	if !fi.IsDir() {
		t.Error("IsDir should be true for type=dir")
	}
}

func TestItemToFileInfoModTimeFromCreation(t *testing.T) {
	created := time.Now().Add(-24 * time.Hour)
	item := &tbox.MergedItemDto{
		Name:         "old.txt",
		Type:         "file",
		CreationTime: created,
		// ModificationTime is zero
	}
	fi := itemToFileInfo(item)
	if !fi.ModTime().Equal(created) {
		t.Errorf("ModTime should fall back to CreationTime: got %v, want %v", fi.ModTime(), created)
	}
}

func TestItemToFileInfoModTimeFallsBackToNow(t *testing.T) {
	before := time.Now().Add(-time.Second)
	item := &tbox.MergedItemDto{
		Name: "notime.txt",
		Type: "file",
		// Both times are zero
	}
	fi := itemToFileInfo(item)
	if fi.ModTime().Before(before) {
		t.Errorf("ModTime should fall back to time.Now(), got %v", fi.ModTime())
	}
}
