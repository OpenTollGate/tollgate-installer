package main

// Unguessable job ids (finding 2 on OpenTollGate/tollgate-installer#46).
// Split into its own file so the RED capture for the other items is not
// masked by this file's build error (newJobID does not exist yet).

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"
)

// mainGoSrc is the shipped main.go, for the static guard below.
//
//go:embed main.go
var mainGoSrc string

// TestJobIDsAreCryptoRandomAndClockIndependent mints a burst of job IDs and
// asserts they are unguessable.
//
// The old generator was fmt.Sprintf("%d", time.Now().UnixNano()%100000000):
// constant within each 100 ms tick, so a ±1 s clock window yields ~21
// candidate IDs and any local process (or any loopback-origin page the CORS
// allowlist trusts) could harvest a pending job's credential with a few dozen
// GETs. All draws below happen within a handful of milliseconds — under the
// old generator the second draw already repeats the first, and the clock
// enumeration below finds the live ID.
func TestJobIDsAreCryptoRandomAndClockIndependent(t *testing.T) {
	const draws = 4096
	seen := make(map[string]bool, draws)
	ids := make([]string, 0, draws)
	for i := 0; i < draws; i++ {
		id, err := newJobID()
		if err != nil {
			t.Fatalf("newJobID: %v", err)
		}
		if seen[id] {
			t.Fatalf("newJobID repeated %q within %d draws (draw %d) — a value that repeats is a value an attacker can enumerate", id, draws, i+1)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for i, id := range ids {
		if len(id) != 32 {
			t.Fatalf("id %d = %q: %d chars, want 32 (16 bytes hex)", i, id, len(id))
		}
		if _, err := hex.DecodeString(id); err != nil {
			t.Fatalf("id %d = %q is not hex: %v", i, id, err)
		}
		if id != strings.ToLower(id) {
			t.Fatalf("id %d = %q must be lower-case hex", i, id)
		}
	}

	// Two ids minted back to back in the same instant must differ, and neither
	// may be recoverable from the clock: enumerate the legacy candidate set
	// (±1 s at the old 100 ms granularity) from the real clock and assert no
	// live id is in it.
	before := time.Now()
	a, err := newJobID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newJobID()
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if a == b {
		t.Fatalf("two ids minted in the same instant are identical: %q", a)
	}
	legacy := map[string]bool{}
	for ts := before.UnixNano() - int64(time.Second); ts <= after.UnixNano()+int64(time.Second); ts += int64(100 * time.Millisecond) {
		legacy[fmt.Sprintf("%d", ts%100000000)] = true
	}
	for _, id := range []string{a, b} {
		if legacy[id] {
			t.Fatalf("job id %q is a clock-derived value in the ±1 s enumeration window — enumerable from the clock", id)
		}
	}

	// Static guard (AST, so the doc comments that quote the old formula do not
	// trip it): no modulo expression in main.go may involve the nanosecond
	// clock or the legacy modulus.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", mainGoSrc, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || bin.Op != token.REM {
			return true
		}
		expr := mainGoSrc[fset.Position(bin.Pos()).Offset:fset.Position(bin.End()).Offset]
		if strings.Contains(expr, "UnixNano") || strings.Contains(expr, "100000000") {
			t.Errorf("main.go still derives a job id from the clock: %q at %s", expr, fset.Position(bin.Pos()))
		}
		return true
	})
}
