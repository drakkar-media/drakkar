package fuse

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrReadOnly = errors.New("read only")
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid operation")
)

type Namespace string

const (
	NamespaceIDs               Namespace = ".ids"
	NamespaceCompletedSymlinks Namespace = "completed-symlinks"
	NamespaceContent           Namespace = "content"
	NamespaceNZBs              Namespace = "nzbs"
)

type Root struct {
	mu        sync.RWMutex
	timestamp time.Time
}

func NewRoot() *Root {
	return &Root{timestamp: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (r *Root) TopLevel() []string {
	out := []string{string(NamespaceIDs), string(NamespaceCompletedSymlinks), string(NamespaceContent), string(NamespaceNZBs)}
	sort.Strings(out)
	return out
}

func (r *Root) Timestamp() time.Time {
	return r.timestamp
}

func (r *Root) Allowed(path, op string, contentWritable bool) error {
	path = strings.Trim(path, "/")
	if path == "" {
		switch op {
		case "lookup", "getattr", "readdir":
			return nil
		default:
			return ErrReadOnly
		}
	}
	root := strings.Split(path, "/")[0]
	switch root {
	case ".ids":
		return allow(op, "lookup", "getattr", "readdir", "open", "read")
	case "completed-symlinks":
		return allow(op, "lookup", "getattr", "readdir", "readlink")
	case "content":
		if op == "unlink" && contentWritable {
			return nil
		}
		return allow(op, "lookup", "getattr", "readdir", "open", "read", "release")
	case "nzbs":
		return allow(op, "lookup", "getattr", "readdir", "open", "read", "create", "write", "flush", "release", "unlink")
	default:
		return ErrNotFound
	}
}

func allow(op string, allowed ...string) error {
	for _, item := range allowed {
		if op == item {
			return nil
		}
	}
	if op == "rename" || op == "mkdir" || op == "truncate" || op == "symlink" || op == "link" || op == "chmod" || op == "chown" {
		return ErrReadOnly
	}
	return ErrInvalid
}
