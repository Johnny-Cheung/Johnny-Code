package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)



type FileStateCache struct {
	mu      sync.Mutex
	entries map[string]*fileStateEntry
}

type fileStateEntry struct {
	Content string
	Mtime   int64 
}

func NewFileStateCache() *FileStateCache {
	return &FileStateCache{
		entries: make(map[string]*fileStateEntry),
	}
}


func (c *FileStateCache) Record(filePath string, content string, mtime int64) {
	abs := normalizePath(filePath)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[abs] = &fileStateEntry{Content: content, Mtime: mtime}
}




func (c *FileStateCache) Check(filePath string) (bool, string) {
	abs := normalizePath(filePath)
	c.mu.Lock()
	entry, exists := c.entries[abs]
	c.mu.Unlock()

	if !exists {
		return false, fmt.Sprintf("Error: file has not been read yet. Read it first before editing.")
	}

	info, err := os.Stat(abs)
	if err != nil {
		
		return true, ""
	}
	currentMtime := info.ModTime().UnixMilli()
	if currentMtime > entry.Mtime {
		return false, fmt.Sprintf("Error: file has been modified since last read. Read it again before editing.")
	}

	return true, ""
}


func (c *FileStateCache) Update(filePath string, newContent string) {
	abs := normalizePath(filePath)
	info, err := os.Stat(abs)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[abs] = &fileStateEntry{
		Content: newContent,
		Mtime:   info.ModTime().UnixMilli(),
	}
}

func normalizePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
