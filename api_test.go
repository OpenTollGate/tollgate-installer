package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestHandleScan(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/scan", nil)
	w := httptest.NewRecorder()

	handleScan(w, req)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("handleScan: status code = %d, want 200", resp.StatusCode)
	}

	var respJSON map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		t.Fatalf("handleScan: failed to decode JSON: %v", err)
	}

	if _, ok := respJSON["routers"].([]interface{}); !ok {
		t.Error("handleScan: response should have 'routers' array")
	}
}

func TestHandleIndex(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handleIndex(w, req)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("handleIndex: status code = %d, want 200", resp.StatusCode)
	}

	if resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("handleIndex: Content-Type = %q, want %q", resp.Header.Get("Content-Type"), "text/html; charset=utf-8")
	}
}

func TestDeployRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     deployRequest
		wantErr bool
	}{
		{
			name:    "missing IP and password",
			req:     deployRequest{IP: "", Password: "", LNURL: "test@wallet.app"},
			wantErr: true,
		},
		{
			// Fresh-reset OpenWrt routers ship with an empty root password —
			// an empty password is no longer a validation error.
			name:    "empty password (fresh reset)",
			req:     deployRequest{IP: "192.168.1.1", Password: "", LNURL: "test@wallet.app"},
			wantErr: false,
		},
		{
			name:    "missing IP",
			req:     deployRequest{IP: "", Password: "password", LNURL: "test@wallet.app"},
			wantErr: true,
		},
		{
			name:    "invalid lightning address",
			req:     deployRequest{IP: "192.168.1.1", Password: "password", LNURL: "invalid"},
			wantErr: true,
		},
		{
			name:    "valid lightning address",
			req:     deployRequest{IP: "192.168.1.1", Password: "password", LNURL: "test@wallet.app"},
			wantErr: false,
		},
		{
			name:    "valid lnurl",
			req:     deployRequest{IP: "192.168.1.1", Password: "password", LNURL: "lnurl1dp68gurn8ghj7um5wfnz7rrfh"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Empty password is valid (fresh-reset router); only a missing IP
			// short-circuits validation.
			if tt.req.IP == "" {
				if !tt.wantErr {
					t.Errorf("%s: expected error, got none", tt.name)
				}
				return
			}

			got := validLightningAddress(tt.req.LNURL)
			if !got && !tt.wantErr {
				t.Errorf("%s: validLightningAddress returned false, want true for valid input", tt.name)
			}
			if got && tt.wantErr {
				t.Errorf("%s: validLightningAddress returned true, want false for invalid input", tt.name)
			}
		})
	}
}

func TestDeployRequestDefaults(t *testing.T) {
	req := deployRequest{
		IP:       "192.168.1.1",
		Password: "password",
		LNURL:    "test@wallet.app",
	}

	// Omitted (nil) advanced fields resolve to the server-side defaults.
	if req.DevSplit != nil {
		t.Errorf("default DevSplit = %v, want nil (unset)", *req.DevSplit)
	}
	if req.Margin != nil {
		t.Errorf("default Margin = %v, want nil (unset)", *req.Margin)
	}
	ds, m := req.resolvedAdvanced()
	if ds != defaultDevSplit {
		t.Errorf("resolved DevSplit = %d, want %d", ds, defaultDevSplit)
	}
	if m != defaultMargin {
		t.Errorf("resolved Margin = %d, want %d", m, defaultMargin)
	}

	// An explicit 0 must be honoured, not replaced by the default.
	zero := 0
	explicit := deployRequest{DevSplit: &zero, Margin: &zero}
	ds, m = explicit.resolvedAdvanced()
	if ds != 0 || m != 0 {
		t.Errorf("explicit 0 resolved to (devSplit=%d, margin=%d), want (0,0)", ds, m)
	}

	if req.Mint != "" {
		t.Errorf("default Mint = %q, want empty", req.Mint)
	}
}

func TestStepInitialization(t *testing.T) {
	steps := deploySteps()

	if len(steps) == 0 {
		t.Error("deploySteps should not return empty steps")
	}

	expectedSteps := []string{"verify", "stage", "flash", "firmware", "password", "upstream", "install", "brand", "portal", "lnurl", "services", "health"}
	if len(steps) != len(expectedSteps) {
		t.Errorf("deploySteps returned %d steps, want %d: %v", len(steps), len(expectedSteps), expectedSteps)
	}
	for i, expected := range expectedSteps {
		if i >= len(steps) {
			t.Errorf("missing step %d: %s", i, expected)
			continue
		}
		if steps[i].Name != expected {
			t.Errorf("step %d: Name = %q, want %q", i, steps[i].Name, expected)
		}
		if steps[i].Status != "pending" {
			t.Errorf("step %d: Status = %q, want %q", i, steps[i].Status, "pending")
		}
	}
}

// TestDeployStepIndexGuard is a STATIC guard over the hardcoded step indices
// in deploy.go. runDeployment drives progress by calling setStep(i,...) and
// jobFail(job, i,...) with LITERAL int indices into the deploySteps() slice.
// Inserting (or removing) a step shifts every later index, and a single
// missed site silently misreports progress (a step that never turns "done",
// or one that reports under the wrong name). Rather than relying on a human
// to renumber all ~45 sites by eye, this test parses the real deploy.go
// source (via the go:embed in pins_test.go) and mechanically asserts:
//
//  1. deploySteps() returns EXACTLY 12 steps.
//  2. every literal index passed to setStep/jobFail is < len(steps) — an
//     out-of-range index would panic or (worse) silently no-op via the
//     `if i < len(j.Steps)` guard in Job.setStep.
//  3. every step index 0..11 is referenced by at least one setStep/jobFail
//     literal — an index that no call site targets means that step can
//     never leave "pending", which is exactly how a missed renumber shows
//     up (e.g. two steps both reporting index 1 after 'stage' was inserted).
func TestDeployStepIndexGuard(t *testing.T) {
	steps := deploySteps()

	// 1. Exactly 12 steps: verify(0) stage(1) flash(2) firmware(3)
	//    password(4) upstream(5) install(6) brand(7) portal(8) lnurl(9)
	//    services(10) health(11).
	if len(steps) != 12 {
		t.Fatalf("deploySteps() returned %d steps, want exactly 12 (stage inserted at index 1)", len(steps))
	}

	// Collect every literal step index used by setStep(...)/jobFail(job,...)
	// call sites in deploy.go. Uses the same go/ast technique as
	// parsePinnedURLConsts in pins_test.go — real syntax, not text matching.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "deploy.go", deployGoSrc, 0)
	if err != nil {
		t.Fatalf("parsing deploy.go: %v", err)
	}

	used := map[int]bool{}
	var runningSeq []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var idxArg ast.Expr
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			// job.setStep(i, status, detail): i is arg 0.
			if fn.Sel.Name == "setStep" && len(call.Args) >= 1 {
				idxArg = call.Args[0]
			}
		case *ast.Ident:
			// jobFail(job, i, detail, err): i is arg 1.
			if fn.Name == "jobFail" && len(call.Args) >= 2 {
				idxArg = call.Args[1]
			}
		}
		if idxArg == nil {
			return true
		}
		lit, ok := idxArg.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			// Non-literal index (e.g. the `step` parameter inside jobFail)
			// is fine — its value came from a literal call site we already
			// captured.
			return true
		}
		i, err := strconv.Atoi(lit.Value)
		if err != nil {
			return true
		}
		used[i] = true
		if i < 0 || i >= len(steps) {
			t.Errorf("deploy.go uses step index %d (>= len(deploySteps())=%d) at %s — renumber it",
				i, len(steps), fset.Position(call.Pos()))
		}
		// Track setStep(...,"running",...) markers in source order.
		if fn, ok := call.Fun.(*ast.SelectorExpr); ok && fn.Sel.Name == "setStep" && len(call.Args) >= 2 {
			if st, ok := call.Args[1].(*ast.BasicLit); ok {
				if s, err := strconv.Unquote(st.Value); err == nil && s == "running" {
					runningSeq = append(runningSeq, i)
				}
			}
		}
		return true
	})

	// 3. Every step position must have at least one reporting call site.
	for i := range steps {
		if !used[i] {
			t.Errorf("step index %d (%q) has NO setStep/jobFail call site — it can never leave \"pending\"; a renumber was likely missed",
				i, steps[i].Name)
		}
	}

	// 4. The "running" markers must fire exactly once per step, in ascending
	//    source order 0..len-1. runDeployment is strictly linear (verify →
	//    stage → flash → ... → health), so the literal indices of its
	//    setStep(i,"running") calls MUST read back 0,1,2,...,11 in file
	//    order. A duplicate (two steps reporting under the same index) or a
	//    gap (a step that never turns running) is exactly the silent
	//    misreporting a missed renumber produces — and a pure coverage
	//    check cannot catch a duplicate.
	wantSeq := make([]int, len(steps))
	for i := range wantSeq {
		wantSeq[i] = i
	}
	if len(runningSeq) != len(wantSeq) {
		t.Errorf("deploy.go has %d setStep(...,\"running\") markers, want %d (one per step): got %v",
			len(runningSeq), len(wantSeq), runningSeq)
	} else {
		for i := range wantSeq {
			if runningSeq[i] != wantSeq[i] {
				t.Errorf("running-marker sequence = %v, want %v — a step index was shifted to the wrong slot",
					runningSeq, wantSeq)
				break
			}
		}
	}
}
