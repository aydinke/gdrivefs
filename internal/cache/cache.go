package cache

import (
	"sync"
	"time"
)

type FileMeta struct {
	ID          string
	Name        string
	IsDir       bool
	Size        int64
	ModTime     time.Time
	ParentID    string
	MimeType    string
	MD5Checksum string
}

type Cache struct {
	mu       sync.RWMutex
	byID     map[string]*FileMeta
	byParent map[string][]*FileMeta
	ttl      time.Duration
	created  time.Time
}

func New(ttl time.Duration) *Cache {
	return &Cache{
		byID:     make(map[string]*FileMeta),
		byParent: make(map[string][]*FileMeta),
		ttl:      ttl,
		created:  time.Now(),
	}
}

func (c *Cache) Get(id string) (*FileMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.expired() {
		return nil, false
	}
	f, ok := c.byID[id]
	return f, ok
}

func (c *Cache) GetByName(parentID, name string) (*FileMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.expired() {
		return nil, false
	}
	children := c.byParent[parentID]
	for _, f := range children {
		if f.Name == name {
			return f, true
		}
	}
	return nil, false
}

func (c *Cache) ListChildren(parentID string) ([]*FileMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.expired() {
		return nil, false
	}
	children, ok := c.byParent[parentID]
	return children, ok
}

func (c *Cache) Put(f *FileMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanup()
	c.byID[f.ID] = f
	c.byParent[f.ParentID] = append(c.byParent[f.ParentID], f)
}

func (c *Cache) PutAll(files []*FileMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanup()
	for _, f := range files {
		c.byID[f.ID] = f
		c.byParent[f.ParentID] = append(c.byParent[f.ParentID], f)
	}
}

func (c *Cache) Delete(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.byID[id]
	if !ok {
		return
	}
	delete(c.byID, id)
	children := c.byParent[f.ParentID]
	for i, child := range children {
		if child.ID == id {
			c.byParent[f.ParentID] = append(children[:i], children[i+1:]...)
			break
		}
	}
}

func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID = make(map[string]*FileMeta)
	c.byParent = make(map[string][]*FileMeta)
	c.created = time.Now()
}

func (c *Cache) expired() bool {
	return time.Since(c.created) > c.ttl
}

func (c *Cache) cleanup() {
	if c.expired() {
		c.byID = make(map[string]*FileMeta)
		c.byParent = make(map[string][]*FileMeta)
		c.created = time.Now()
	}
}
