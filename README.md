# btree-index

`btree-index` is a small, dependency-free Go library and command-line tool that
provides a persistent ordered key/value index. Data is stored in a single page
file: a fixed-size pager manages pages and a free list on disk, a B+tree keeps
keys ordered and supports insertion with node splitting, point lookups, ordered
range scans, and deletion with sibling borrowing and node merging, and a thin
facade (`index`) exposes `Put`/`Get`/`Delete`/`Scan`. Reopening the same file
returns the data that was persisted by a previous session, so the index behaves
like a tiny on-disk sorted map.

## Packages

- `internal/pager` — fixed-size page file manager (header page + persisted free
  list, allocate/free/read/write, reopen).
- `internal/btree` — B+tree over pages (serialised nodes, configurable order,
  insert/lookup/delete/scan).
- `internal/index` — facade over the pager and btree (`Open`, `Put`, `Get`,
  `Delete`, `Scan`, `Close`).

## CLI

```
btree-index --db <path> put <key> <value>
btree-index --db <path> get <key>
btree-index --db <path> del <key>
btree-index --db <path> scan <start> <end>
```

## Build and test

```
GOTOOLCHAIN=local CGO_ENABLED=0 go build ./...
GOTOOLCHAIN=local CGO_ENABLED=0 go test ./...
GOTOOLCHAIN=local go vet ./...
```

The project builds with the standard library only (Go 1.21).
