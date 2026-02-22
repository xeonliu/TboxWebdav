package tbox

import (
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// credExpiryBuffer is the number of seconds before the token expiry at which
// the cached credential is considered stale and will be refreshed proactively.
const credExpiryBuffer = 30 * time.Second

// credCacheEntry wraps a SpaceCred with its fetch timestamp.
type credCacheEntry struct {
	cred    *SpaceCred
	fetched time.Time
}

// CredCache caches SpaceCred values keyed by userToken.
type CredCache struct {
	mu      sync.RWMutex
	entries map[string]*credCacheEntry
}

// NewCredCache creates an empty CredCache.
func NewCredCache() *CredCache {
	return &CredCache{entries: make(map[string]*credCacheEntry)}
}

// Get returns a cached SpaceCred, or nil if absent / expired.
func (c *CredCache) Get(userToken string) *SpaceCred {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[userToken]
	if !ok {
		return nil
	}
	// Evict credExpiryBuffer before the token actually expires to avoid races.
	ttl := time.Duration(e.cred.ExpiresIn)*time.Second - credExpiryBuffer
	if ttl <= 0 {
		ttl = 0
	}
	if time.Since(e.fetched) > ttl {
		return nil
	}
	return e.cred
}

// Set stores a SpaceCred under userToken.
func (c *CredCache) Set(userToken string, cred *SpaceCred) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[userToken] = &credCacheEntry{cred: cred, fetched: time.Now()}
}

// TokenCache caches userToken values keyed by JAAuthCookie.
type TokenCache struct {
	mu      sync.RWMutex
	entries map[string]string
}

// NewTokenCache creates an empty TokenCache.
func NewTokenCache() *TokenCache {
	return &TokenCache{entries: make(map[string]string)}
}

// Get returns the cached userToken for a given jaCookie, or "" if absent.
func (c *TokenCache) Get(jaCookie string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.entries[jaCookie]
}

// Set stores userToken under jaCookie.
func (c *TokenCache) Set(jaCookie, userToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[jaCookie] = userToken
}

// quotaCacheEntry wraps a SpaceQuotaInfo with its fetch timestamp.
type quotaCacheEntry struct {
	quota   *SpaceQuotaInfo
	fetched time.Time
}

// QuotaCache caches SpaceQuotaInfo values keyed by userToken, with a 15-minute TTL.
type QuotaCache struct {
	mu      sync.RWMutex
	entries map[string]*quotaCacheEntry
}

// NewQuotaCache creates an empty QuotaCache.
func NewQuotaCache() *QuotaCache {
	return &QuotaCache{entries: make(map[string]*quotaCacheEntry)}
}

// Get returns a cached SpaceQuotaInfo, or nil if absent / older than 15 minutes.
func (c *QuotaCache) Get(userToken string) *SpaceQuotaInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[userToken]
	if !ok {
		return nil
	}
	if time.Since(e.fetched) > 15*time.Minute {
		return nil
	}
	return e.quota
}

// Set stores a SpaceQuotaInfo under userToken.
func (c *QuotaCache) Set(userToken string, quota *SpaceQuotaInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[userToken] = &quotaCacheEntry{quota: quota, fetched: time.Now()}
}

// Global singleton cache instances shared across all requests.
var (
	GlobalCredCache  = NewCredCache()
	GlobalTokenCache = NewTokenCache()
	GlobalQuotaCache = NewQuotaCache()
	GlobalDirCache   = NewDirCache()
)

// --------------------------------------------------------------------------
// DirCache — caches GetItemInfo and ListItems results
// --------------------------------------------------------------------------

// dirCacheTTL is the TTL for directory listing and item-info cache entries.
// After this duration entries are considered stale and re-fetched from the API.
const dirCacheTTL = 30 * time.Second

// itemInfoEntry is a cache slot for a single MergedItemDto.
type itemInfoEntry struct {
	item    *MergedItemDto
	expires time.Time
}

// dirListEntry is a cache slot for one page of a directory listing.
type dirListEntry struct {
	list    *ItemListDto
	expires time.Time
}

// DirCache caches GetItemInfo and ListItems API results with a short TTL and
// supports path-based invalidation for mutation operations (PUT/DELETE/MKCOL/COPY/MOVE).
type DirCache struct {
	mu       sync.RWMutex
	itemInfo map[string]*itemInfoEntry
	dirLists map[string]*dirListEntry
}

// NewDirCache creates an empty DirCache.
func NewDirCache() *DirCache {
	return &DirCache{
		itemInfo: make(map[string]*itemInfoEntry),
		dirLists: make(map[string]*dirListEntry),
	}
}

// GetItemInfo returns the cached MergedItemDto for path, or nil if absent/expired.
func (c *DirCache) GetItemInfo(p string) *MergedItemDto {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.itemInfo[p]
	if !ok || time.Now().After(e.expires) {
		return nil
	}
	return e.item
}

// SetItemInfo stores item in the cache under path.
func (c *DirCache) SetItemInfo(p string, item *MergedItemDto) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.itemInfo[p] = &itemInfoEntry{item: item, expires: time.Now().Add(dirCacheTTL)}
}

// listKey returns the map key for a directory listing page.
func listKey(p string, page, pageSize int) string {
	return p + "\x00" + strconv.Itoa(page) + "\x00" + strconv.Itoa(pageSize)
}

// GetDirList returns a cached ItemListDto, or nil if absent/expired.
func (c *DirCache) GetDirList(p string, page, pageSize int) *ItemListDto {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.dirLists[listKey(p, page, pageSize)]
	if !ok || time.Now().After(e.expires) {
		return nil
	}
	return e.list
}

// SetDirList stores list in the cache under (path, page, pageSize).
func (c *DirCache) SetDirList(p string, page, pageSize int, list *ItemListDto) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirLists[listKey(p, page, pageSize)] = &dirListEntry{list: list, expires: time.Now().Add(dirCacheTTL)}
}

// Invalidate removes all cached entries for the given path and its parent
// directory. Call this after any successful mutation.
func (c *DirCache) Invalidate(p string) {
	parent := path.Dir(p)
	// Guard against path.Dir("") == "." edge case.
	if parent == "." {
		parent = "/"
	}

	// Listing prefix for a directory: all keys beginning with "dir\x00page\x00size"
	// share the directory path as prefix up to the first \x00.
	listPrefixPath := p + "\x00"
	listPrefixParent := parent + "\x00"

	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove exact item info entries.
	delete(c.itemInfo, p)
	delete(c.itemInfo, parent)

	// Remove all dir-list pages for path and parent.
	for key := range c.dirLists {
		if strings.HasPrefix(key, listPrefixPath) || strings.HasPrefix(key, listPrefixParent) {
			delete(c.dirLists, key)
		}
	}
}
