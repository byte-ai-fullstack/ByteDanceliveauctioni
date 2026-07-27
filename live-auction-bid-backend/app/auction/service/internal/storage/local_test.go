package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStoragePutDeleteAndRejectTraversal(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStorage(LocalConfig{RootDir: root, PublicBaseURL: "http://assets.local"})
	if err != nil {
		t.Fatalf("new local storage: %v", err)
	}
	object, err := store.PutObject(context.Background(), PutObjectInput{
		ObjectKey: "rooms/room-a/item.txt",
		Reader:    strings.NewReader("hello"),
	})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	if object.Provider != "local" || object.PublicURL != "http://assets.local/rooms/room-a/item.txt" {
		t.Fatalf("stored object mismatch: %+v", object)
	}
	if data, err := os.ReadFile(filepath.Join(root, "rooms", "room-a", "item.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("stored file mismatch: data=%q err=%v", string(data), err)
	}
	if _, err := store.PutObject(context.Background(), PutObjectInput{ObjectKey: "../escape.txt", Reader: strings.NewReader("bad")}); err == nil {
		t.Fatal("path traversal should be rejected")
	}
	if err := store.DeleteObject(context.Background(), "rooms/room-a/item.txt"); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "rooms", "room-a", "item.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted, err=%v", err)
	}
}
