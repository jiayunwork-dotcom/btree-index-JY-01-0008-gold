// Command example demonstrates building a persistent ordered index, inserting
// key/value pairs, looking them up, deleting some, and performing an ordered
// range scan. It does not require any network access.
//
// Run it from the repository root:
//
//	go run ./example
package main

import (
	"fmt"
	"log"
	"os"

	"btree-index/internal/index"
)

func main() {
	path := "example/words.idx"
	// Remove any stale file from a previous run so the demo is reproducible.
	_ = os.Remove(path)

	idx, err := index.Open(path)
	if err != nil {
		log.Fatalf("open index: %v", err)
	}
	defer func() {
		if cerr := idx.Close(); cerr != nil {
			log.Fatalf("close index: %v", cerr)
		}
	}()

	words := []struct {
		key   string
		value string
	}{
		{"apple", "fruit"},
		{"banana", "fruit"},
		{"carrot", "vegetable"},
		{"durian", "fruit"},
		{"eggplant", "vegetable"},
		{"fig", "fruit"},
	}
	for _, w := range words {
		if perr := idx.Put([]byte(w.key), []byte(w.value)); perr != nil {
			log.Fatalf("put %q: %v", w.key, perr)
		}
	}

	// Look up a value.
	v, ok, err := idx.Get([]byte("banana"))
	if err != nil {
		log.Fatalf("get: %v", err)
	}
	if !ok {
		log.Fatalf("banana missing")
	}
	fmt.Printf("banana => %s\n", v)

	// Delete a key.
	if derr := idx.Delete([]byte("durian")); derr != nil {
		log.Fatalf("delete: %v", derr)
	}

	// Ordered range scan over the surviving keys.
	pairs, err := idx.Scan([]byte("a"), []byte("z"))
	if err != nil {
		log.Fatalf("scan: %v", err)
	}
	fmt.Println("remaining (ordered):")
	for _, p := range pairs {
		fmt.Printf("  %s = %s\n", p.Key, p.Value)
	}

	count, err := idx.Count()
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("total pairs: %d\n", count)
}
