package tbox

import (
	"sync"
	"time"
)

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
	// Evict 30 s before the token actually expires to avoid races.
	ttl := time.Duration(e.cred.ExpiresIn)*time.Second - 30*time.Second
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
)
