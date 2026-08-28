package lite

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
)

type queryCacheEntry struct {
	key    string
	vector []float32
}
type queryVectorCache struct {
	mu         sync.Mutex
	capacity   int
	generation string
	order      *list.List
	entries    map[string]*list.Element
}

func newQueryVectorCache(capacity int) *queryVectorCache {
	if capacity < 1 {
		capacity = 1
	}
	if capacity > 4096 {
		capacity = 4096
	}
	return &queryVectorCache{capacity: capacity, order: list.New(), entries: map[string]*list.Element{}}
}
func normalizedQueryHash(query string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("sha256:%x", sum[:])
}
func (c *queryVectorCache) get(generation, query string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.switchGeneration(generation)
	element, ok := c.entries[normalizedQueryHash(query)]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return append([]float32(nil), element.Value.(queryCacheEntry).vector...), true
}
func (c *queryVectorCache) put(generation, query string, vector []float32) {
	if strings.TrimSpace(query) == "" || detectSecretLike(query) || detectInstructionLike(query) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.switchGeneration(generation)
	key := normalizedQueryHash(query)
	if element, ok := c.entries[key]; ok {
		element.Value = queryCacheEntry{key: key, vector: append([]float32(nil), vector...)}
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(queryCacheEntry{key: key, vector: append([]float32(nil), vector...)})
	c.entries[key] = element
	for c.order.Len() > c.capacity {
		last := c.order.Back()
		delete(c.entries, last.Value.(queryCacheEntry).key)
		c.order.Remove(last)
	}
}
func (c *queryVectorCache) switchGeneration(generation string) {
	if c.generation == generation {
		return
	}
	c.generation = generation
	c.order.Init()
	c.entries = map[string]*list.Element{}
}
