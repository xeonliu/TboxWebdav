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

// readChunkSize is the size of each read from the backend stream.
// Smaller values reduce latency to first byte; 32 KiB is a good default.
const readChunkSize = 32 * 1024

// WriteTo writes bytes [offset, offset+length) of filePath to w.
//
// Cache hits are served directly from memory.
//
// On a cache miss the block is fetched from the backend via opener. Bytes are
// forwarded to w in readChunkSize increments as they arrive, so the client
// starts receiving data immediately instead of waiting for the full block.
// If w returns a write error (e.g. "broken pipe" when a media player closes
// the connection early) we keep reading from the backend until the block is
// complete so that the completed block can be cached.  The next (immediate)
// retry from the client is then served from cache with no backend round-trip.
//
// Sequential reads reuse a single open HTTP stream across consecutive cache
// misses to avoid redundant connections.
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

		// How many bytes of this block the current request needs.
		toCopy := int(remaining)
		if toCopy > blockLen-blockOff {
			toCopy = blockLen - blockOff
		}

		c.mu.Lock()
		data := c.getBlock(filePath, blockNum)
		c.mu.Unlock()

		if data != nil {
			slog.Debug("filecache: hit", "file", filePath, "block", blockNum)
			// Cache hit: close any open backend stream (it is no longer at the
			// right position for the block that follows).
			if stream != nil {
				stream.Close()
				stream = nil
				streamAt = -1
			}
			if _, err := w.Write(data[blockOff : blockOff+toCopy]); err != nil {
				return err
			}
		} else {
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

			// Read the block in small chunks, writing to the client as bytes
			// arrive so the client is not blocked waiting for the full block.
			// clientErr records any write failure but does NOT abort the read —
			// we finish the block so it can be cached for the immediate retry.
			blockBuf := make([]byte, blockLen)
			totalRead := 0
			var clientErr error
			clientEnd := blockOff + toCopy // exclusive upper bound within the block
			tmp := make([]byte, readChunkSize)

			for totalRead < blockLen {
				want := blockLen - totalRead
				if want > readChunkSize {
					want = readChunkSize
				}
				n, readErr := stream.Read(tmp[:want])
				if n > 0 {
					// Accumulate into blockBuf for caching.
					copy(blockBuf[totalRead:], tmp[:n])

					// Write directly from tmp the bytes that fall within the
					// client's requested range [blockOff, clientEnd).
					chunkStart := 0
					if totalRead < blockOff {
						chunkStart = blockOff - totalRead
					}
					chunkEnd := n
					if totalRead+n > clientEnd {
						chunkEnd = clientEnd - totalRead
					}
					if chunkStart < chunkEnd && clientErr == nil {
						if _, err := w.Write(tmp[chunkStart:chunkEnd]); err != nil {
							// Record the error but continue reading so we can
							// complete and cache the block.
							clientErr = err
						}
					}

					totalRead += n
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					stream.Close()
					stream = nil
					streamAt = -1
					return fmt.Errorf("filecache: read block %d: %w", blockNum, readErr)
				}
			}

			if totalRead == blockLen {
				// Complete block: cache it and mark the stream as advanced.
				c.mu.Lock()
				c.putBlock(filePath, blockNum, blockBuf)
				c.mu.Unlock()
				streamAt = blockEnd
			} else {
				// Partial block (backend returned less data than expected).
				// Do not cache so the next request re-fetches clean data.
				slog.Warn("filecache: partial block read, not caching",
					"file", filePath, "block", blockNum,
					"got", totalRead, "want", blockLen)
				stream.Close()
				stream = nil
				streamAt = -1
			}

			if clientErr != nil {
				return clientErr
			}
		}

		pos += int64(toCopy)
		remaining -= int64(toCopy)
	}
	return nil
}
