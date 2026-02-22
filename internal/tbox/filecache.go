package tbox

import (
	"container/list"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// fileCacheBlockSize is the size of each cached file data block (4 MiB).
const fileCacheBlockSize = 4 * 1024 * 1024

// fileCacheEntry holds one LRU cache entry for a single block of file data.
type fileCacheEntry struct {
	key  string
	data []byte
}

// FileStreamOpener opens an HTTP range stream beginning at start (inclusive).
// end == -1 means "fetch to end of file".
type FileStreamOpener func(start, end int64) (io.ReadCloser, error)

// FileCache is a thread-safe LRU block cache for file content.
// It divides each file into fixed-size blocks and caches recently accessed
// blocks in memory, reducing redundant HTTP range requests to the backend.
type FileCache struct {
	mu        sync.Mutex
	blockSize int
	capacity  int // maximum number of blocks
	index     map[string]*list.Element
	lru       *list.List
}

// NewFileCache creates a FileCache with the given total memory budget in bytes.
// Block size is 4 MiB; the minimum capacity is 4 blocks.
func NewFileCache(totalBytes int) *FileCache {
	capacity := totalBytes / fileCacheBlockSize
	if capacity < 4 {
		capacity = 4
	}
	return &FileCache{
		blockSize: fileCacheBlockSize,
		capacity:  capacity,
		index:     make(map[string]*list.Element),
		lru:       list.New(),
	}
}

// fileCacheKey returns the map key for a given file path and block number.
func fileCacheKey(filePath string, blockNum int64) string {
	return fmt.Sprintf("%s\x00%d", filePath, blockNum)
}

// getBlock returns cached block data, or nil if absent.
// Must be called with c.mu held.
func (c *FileCache) getBlock(filePath string, blockNum int64) []byte {
	key := fileCacheKey(filePath, blockNum)
	elem, ok := c.index[key]
	if !ok {
		return nil
	}
	c.lru.MoveToBack(elem)
	return elem.Value.(*fileCacheEntry).data
}

// putBlock stores a block in the cache, evicting the LRU entry when at capacity.
// Must be called with c.mu held.
func (c *FileCache) putBlock(filePath string, blockNum int64, data []byte) {
	key := fileCacheKey(filePath, blockNum)
	if elem, ok := c.index[key]; ok {
		c.lru.MoveToBack(elem)
		elem.Value.(*fileCacheEntry).data = data
		return
	}
	for c.lru.Len() >= c.capacity {
		oldest := c.lru.Front()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.index, oldest.Value.(*fileCacheEntry).key)
	}
	entry := &fileCacheEntry{key: key, data: data}
	elem := c.lru.PushBack(entry)
	c.index[key] = elem
}

// Invalidate removes all cached blocks for the given file path.
func (c *FileCache) Invalidate(filePath string) {
	prefix := filePath + "\x00"
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, elem := range c.index {
		if strings.HasPrefix(k, prefix) {
			c.lru.Remove(elem)
			delete(c.index, k)
		}
	}
}

// WriteTo writes bytes [offset, offset+length) of filePath to w.
// Cached blocks are served directly; missing blocks are fetched via opener and
// then stored in the cache. Sequential reads reuse a single open HTTP stream
// across consecutive cache misses to avoid redundant connections.
// opener is called with (blockStart, -1) to fetch from blockStart to EOF.
func (c *FileCache) WriteTo(w io.Writer, filePath string, totalSize, offset, length int64, opener FileStreamOpener) error {
	if length <= 0 || offset >= totalSize {
		return nil
	}
	if offset+length > totalSize {
		length = totalSize - offset
	}

	pos := offset
	remaining := length

	var (
		stream   io.ReadCloser
		streamAt int64 = -1 // byte offset where the open stream is positioned
	)
	defer func() {
		if stream != nil {
			stream.Close()
		}
	}()

	for remaining > 0 {
		blockNum := pos / int64(c.blockSize)
		blockOff := int(pos % int64(c.blockSize))
		blockStart := blockNum * int64(c.blockSize)
		blockEnd := blockStart + int64(c.blockSize)
		if blockEnd > totalSize {
			blockEnd = totalSize
		}
		blockLen := int(blockEnd - blockStart)

		c.mu.Lock()
		data := c.getBlock(filePath, blockNum)
		c.mu.Unlock()

		if data == nil {
			slog.Debug("filecache: miss", "file", filePath, "block", blockNum)
			// Open or reuse an HTTP stream positioned at blockStart.
			if streamAt != blockStart {
				if stream != nil {
					stream.Close()
					stream = nil
				}
				var err error
				stream, err = opener(blockStart, -1)
				if err != nil {
					return fmt.Errorf("filecache: open stream at block %d: %w", blockNum, err)
				}
				streamAt = blockStart
			}

			data = make([]byte, blockLen)
			n, err := io.ReadFull(stream, data)
			if err == io.ErrUnexpectedEOF {
				// Backend returned fewer bytes than expected (truncated response or
				// dropped connection). Do not cache the partial block so that a
				// subsequent request will re-fetch clean data from the server.
				data = data[:n]
				stream.Close()
				stream = nil
				streamAt = -1
				slog.Warn("filecache: partial block read, not caching", "file", filePath, "block", blockNum, "got", n, "want", blockLen)
			} else if err != nil {
				stream.Close()
				stream = nil
				streamAt = -1
				return fmt.Errorf("filecache: read block %d: %w", blockNum, err)
			} else {
				streamAt = blockEnd
				// Only cache complete blocks to ensure data consistency.
				c.mu.Lock()
				c.putBlock(filePath, blockNum, data)
				c.mu.Unlock()
			}
		} else {
			slog.Debug("filecache: hit", "file", filePath, "block", blockNum)
			// Cache hit: the open stream (if any) is no longer at the right position
			// for the block after this one, so close it to free the connection.
			if stream != nil {
				stream.Close()
				stream = nil
				streamAt = -1
			}
		}

		available := len(data) - blockOff
		if available <= 0 {
			break
		}
		toCopy := remaining
		if toCopy > int64(available) {
			toCopy = int64(available)
		}
		if _, err := w.Write(data[blockOff : blockOff+int(toCopy)]); err != nil {
			return err
		}
		pos += toCopy
		remaining -= toCopy
	}
	return nil
}
