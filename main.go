// Command btree-index is a small command-line interface over the persistent
// ordered index. It supports inserting, retrieving, deleting and range-scanning
// key/value pairs stored in a page file.
//
// Usage:
//
//	btree-index --db <path> put <key> <value>
//	btree-index --db <path> get <key>
//	btree-index --db <path> del <key>
//	btree-index --db <path> scan <start> <end>
//
// All keys and values are treated as raw byte strings.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"btree-index/internal/index"
)

// reorderArgs moves flag-style arguments (those beginning with '-') to the
// front so that the standard flag parser can consume them regardless of whether
// they appear before or after the subcommand and its positional arguments. Each
// flag is kept together with its immediately following value so that the flag
// package still associates the value correctly.
func reorderArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	pos := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			// Attach the value token unless the flag uses the --name=value form
			// or is a boolean flag (no following non-flag token).
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		pos = append(pos, a)
		i++
	}
	out := make([]string, 0, len(args))
	out = append(out, flags...)
	out = append(out, pos...)
	return out
}

// fail prints an error message and exits with a non-zero status. Only main is
// permitted to call os.Exit; the library reports errors instead.
func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "btree-index: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	os.Args = append(os.Args[:1], reorderArgs(os.Args[1:])...)

	fs := flag.NewFlagSet("btree-index", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the database file")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fail("parse flags: %v", err)
	}
	args := fs.Args()
	if *dbPath == "" {
		fail("missing required --db <path>")
	}
	if len(args) < 1 {
		fail("missing subcommand (put|get|del|scan)")
	}
	cmd := args[0]

	idx, err := index.Open(*dbPath)
	if err != nil {
		fail("open index: %v", err)
	}
	defer func() {
		if cerr := idx.Close(); cerr != nil {
			fail("close index: %v", cerr)
		}
	}()

	switch cmd {
	case "put":
		if len(args) < 3 {
			fail("put requires <key> <value>")
		}
		if err := idx.Put([]byte(args[1]), []byte(args[2])); err != nil {
			fail("put: %v", err)
		}
		fmt.Println("ok")
	case "get":
		if len(args) < 2 {
			fail("get requires <key>")
		}
		val, ok, err := idx.Get([]byte(args[1]))
		if err != nil {
			fail("get: %v", err)
		}
		if !ok {
			fmt.Println("not found")
			return
		}
		fmt.Println(string(val))
	case "del":
		if len(args) < 2 {
			fail("del requires <key>")
		}
		if err := idx.Delete([]byte(args[1])); err != nil {
			fail("del: %v", err)
		}
		fmt.Println("ok")
	case "scan":
		if len(args) < 3 {
			fail("scan requires <start> <end>")
		}
		pairs, err := idx.Scan([]byte(args[1]), []byte(args[2]))
		if err != nil {
			fail("scan: %v", err)
		}
		for _, p := range pairs {
			fmt.Printf("%s=%s\n", p.Key, p.Value)
		}
	default:
		fail("unknown subcommand %q (use put|get|del|scan)", cmd)
	}
}
