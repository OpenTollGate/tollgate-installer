package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// INSTALLER — the step-6 fail-loud refusal must not skip the wireless rollback.
//
// Finding (GATE-2.5 cross-family audit of the PR #40 review draft, card
// t_30d952f1): the refusal added by #40 (`if !pkgOnRouter && fallbackRefusal !=
// nil`) returned immediately after jobFail and so never called the wireless
// rollback that the path it replaced performed.
//
// Sequence on the hardware this targets (a53 bench routers, MT3000/MT6000):
//
//  1. step 5 has already committed and reloaded the STA config for WAN-over-WiFi
//     (deploy.go step 5 / configureSTA) and left /tmp/wireless.pre-tollgate;
//  2. the requested feed tag's asset 404s (feed outage / tag not yet published),
//     and the only other candidate is the pinned GitHub fallback — a DIFFERENT,
//     older release the operator has not opted into — so the refusal fires;
//  3. before this fix the job failed and returned with the radios still in STA
//     mode, so the wizard could no longer re-scan for an upstream SSID (a radio
//     hosting an STA iface cannot scan) and the router needed a physical visit —
//     whereas the pre-#40 fall-through restored the snapshot "so the radios are
//     usable for re-scanning".
//
// DECISION (this PR): the refusal restores the snapshot before failing. A
// refused install is a terminal failure of step 6 exactly like the feed
// last-resort failure below it, and the two sibling failure paths (step 6
// feed-install, step 11 health check) already own that restore. The
// operator-visible contract at that point is "the deploy failed and the router
// is left as it was found, radios usable for a re-run" — that cannot depend on
// which branch failed. With no STA committed by this run (WAN mode) there is no
// snapshot, so the rollback is skipped and nothing is claimed in the log.
//
// Two levels of proof, because neither alone is enough:
//
//   - BEHAVIOURAL (below): the gate/helper functions are driven directly and the
//     rollback is observed through the wirelessRollback seam. The deploy path
//     takes a live *ssh.Client and this package has no SSH seam, so this is the
//     furthest a unit test can drive the refusal in-process.
//   - STATIC (TestDeployFailureSitesRestoreWireless): a go/ast guard over the
//     real deploy.go — in the same spirit as TestDeployStepIndexGuard — so that
//     re-inlining the refusal, or bypassing the helper at one of the failure
//     sites, fails the build instead of silently coming back.
//   - END-TO-END: the fixture-router harness
//     (~/tollgate-artifacts/pre-release-security/harness/run-refusal-rollback.sh)
//     drives an installer binary built from this branch against a fake router
//     with STA committed, and asserts the rollback command reached the fixture
//     AND that its /etc/config/wireless is back to the pre-deploy bytes.

// rollbackCall records one invocation of the wirelessRollback seam.
type rollbackCall struct {
	client *ssh.Client
	// jobStatus is the job's status AT THE MOMENT the rollback ran. It must
	// still be "running": the restore happens before the job is marked failed,
	// so a failing deploy can never be observed as finished while the radios are
	// still committed.
	jobStatus string
}

// stubWirelessRollback replaces the rollback side effect with a recorder for one
// test and returns the recorded calls. The original is restored via t.Cleanup so
// a failing test cannot leak the stub into the rest of the package.
func stubWirelessRollback(t *testing.T, job *Job) *[]rollbackCall {
	t.Helper()
	calls := &[]rollbackCall{}
	orig := wirelessRollback
	wirelessRollback = func(c *ssh.Client) {
		*calls = append(*calls, rollbackCall{client: c, jobStatus: job.Status})
	}
	t.Cleanup(func() { wirelessRollback = orig })
	return calls
}

// realFallbackRefusal returns the actual refusal error step 6's fail-loud gate
// acts on: the pinned GitHub fallback is a different, older release and no
// opt-in was given.
func realFallbackRefusal(t *testing.T) error {
	t.Helper()
	url, err := githubFallbackSelection(pkgFallbackTestArch, ".ipk", false)
	if err == nil || url != "" {
		t.Fatalf("fixture stale: githubFallbackSelection(%s, .ipk, allowFallback=false) = (%q, %v), want the refusal error deploy step 6 acts on",
			pkgFallbackTestArch, url, err)
	}
	return err
}

// jobLogText returns the deploy log as one string (the UI's own order).
func jobLogText(job *Job) string {
	job.mu.Lock()
	defer job.mu.Unlock()
	var b strings.Builder
	for _, e := range job.Log {
		b.WriteString(e.Msg)
		b.WriteString("\n")
	}
	return b.String()
}

// TestRefusalRestoresWirelessSnapshotBeforeFailing drives the refusal with STA
// committed — the exact hardware sequence in the finding — and asserts the
// chosen behaviour: the pre-deploy wireless snapshot IS restored, BEFORE the job
// is marked failed, and the log says so.
func TestRefusalRestoresWirelessSnapshotBeforeFailing(t *testing.T) {
	job := newJob("192.168.1.1")
	// Identity only: the rollback side effect is stubbed, so nothing dereferences
	// it. A non-nil fake lets the test prove the refusal hands the LIVE client to
	// the rollback (not a nil/zero one).
	client := new(ssh.Client)
	calls := stubWirelessRollback(t, job)

	refusal := realFallbackRefusal(t)
	stop := refuseMissingRequestedRelease(job, client, true, false, feedReleaseTag, refusal)

	if !stop {
		t.Fatal("refuseMissingRequestedRelease(...) = false, want true: the caller must stop instead of falling through to the feed install")
	}
	if len(*calls) != 1 {
		t.Fatalf("refusal made %d wireless rollback call(s), want exactly 1 — the refusal must restore the pre-deploy snapshot (this is the whole finding)",
			len(*calls))
	}
	if got := (*calls)[0].client; got != client {
		t.Errorf("rollback was handed %p, want the live deploy client %p", got, client)
	}
	if got := (*calls)[0].jobStatus; got != "running" {
		t.Errorf("rollback ran while the job was already %q; it must run BEFORE the job is marked failed", got)
	}

	if job.Status != "failed" {
		t.Errorf("job.Status = %q, want \"failed\"", job.Status)
	}
	if job.Steps[6].Status != "failed" {
		t.Errorf("step 6 status = %q, want \"failed\"", job.Steps[6].Status)
	}
	if !strings.Contains(job.Steps[6].Detail, feedReleaseTag) {
		t.Errorf("step 6 detail does not name the requested release %q: %q", feedReleaseTag, job.Steps[6].Detail)
	}
	if !strings.Contains(job.Steps[6].Detail, "refusing to install an older package") {
		t.Errorf("step 6 detail does not state the refusal: %q", job.Steps[6].Detail)
	}
	if !strings.Contains(job.Error, refusal.Error()) {
		t.Errorf("job.Error = %q, want it to carry the refusal text %q", job.Error, refusal.Error())
	}

	log := jobLogText(job)
	if !strings.Contains(log, "ERROR: "+refusal.Error()) {
		t.Errorf("deploy log does not carry the refusal:\n%s", log)
	}
	rollbackAt := strings.Index(log, "Rolling back wireless config")
	if rollbackAt < 0 {
		t.Errorf("deploy log does not say the wireless config was restored — the operator sees a failure with the radios left in STA mode:\n%s", log)
	}
	if errAt := strings.Index(log, "ERROR: "); errAt < 0 || rollbackAt < errAt {
		t.Errorf("expected the refusal to be logged before the rollback line (errAt=%d rollbackAt=%d):\n%s", errAt, rollbackAt, log)
	}
}

// TestRefusalWithoutStaDoesNotClaimARollback pins the other half of the
// decision: on a WAN-mode deploy this run committed no radio config and left no
// snapshot, so there is nothing to restore — the refusal must not call the
// rollback and must not tell the operator it did.
func TestRefusalWithoutStaDoesNotClaimARollback(t *testing.T) {
	job := newJob("192.168.1.1")
	calls := stubWirelessRollback(t, job)

	refusal := realFallbackRefusal(t)
	if stop := refuseMissingRequestedRelease(job, new(ssh.Client), false, false, feedReleaseTag, refusal); !stop {
		t.Fatal("refuseMissingRequestedRelease(...) = false, want true: the refusal itself is unchanged by the rollback decision")
	}

	if len(*calls) != 0 {
		t.Errorf("refusal called the wireless rollback %d time(s) on a deploy that committed no STA config", len(*calls))
	}
	log := jobLogText(job)
	if strings.Contains(log, "Rolling back wireless config") {
		t.Errorf("deploy log claims a wireless rollback that did not happen:\n%s", log)
	}
	if job.Status != "failed" || !strings.Contains(log, "ERROR: "+refusal.Error()) {
		t.Errorf("the refusal must fail the job loudly whether or not STA was committed (status=%q):\n%s", job.Status, log)
	}
}

// TestRefusalGateDoesNotFireOnTheHappyPath: the gate must be inert when a
// package landed (pkgOnRouter) or when no refusal was recorded — the success
// path and the plain "nothing downloaded" path below it are unchanged.
func TestRefusalGateDoesNotFireOnTheHappyPath(t *testing.T) {
	refusal := realFallbackRefusal(t)
	for _, tc := range []struct {
		name        string
		pkgOnRouter bool
		refusal     error
	}{
		{"package on router (success path)", true, refusal},
		{"no refusal recorded (feed-only arch)", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := newJob("192.168.1.1")
			calls := stubWirelessRollback(t, job)

			if stop := refuseMissingRequestedRelease(job, new(ssh.Client), true, tc.pkgOnRouter, feedReleaseTag, tc.refusal); stop {
				t.Error("gate fired on a path that must continue: it would fail a deploy that is not refusing anything")
			}
			if len(*calls) != 0 {
				t.Errorf("no-op gate call still rolled back wireless %d time(s)", len(*calls))
			}
			if job.Status != "running" {
				t.Errorf("job.Status = %q, want \"running\" (the deploy continues)", job.Status)
			}
			if log := jobLogText(job); strings.Contains(log, "ERROR: ") || strings.Contains(log, "Rolling back wireless config") {
				t.Errorf("no-op gate call wrote failure text into the log:\n%s", log)
			}
		})
	}
}

// TestRestoreWirelessOnFailureReportsWhatItDid pins the helper the step-6 and
// step-11 terminal failure paths share: it restores exactly when this run
// committed wireless config, and its return value is what callers use to decide
// whether they may tell the operator the radios were restored.
func TestRestoreWirelessOnFailureReportsWhatItDid(t *testing.T) {
	for _, tc := range []struct {
		name         string
		staCommitted bool
		wantCalls    int
		wantRestored bool
	}{
		{"sta committed by this run", true, 1, true},
		{"no sta committed by this run", false, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := newJob("192.168.1.1")
			calls := stubWirelessRollback(t, job)

			restored := restoreWirelessOnFailure(job, new(ssh.Client), tc.staCommitted)

			if len(*calls) != tc.wantCalls {
				t.Errorf("rollback calls = %d, want %d", len(*calls), tc.wantCalls)
			}
			if restored != tc.wantRestored {
				t.Errorf("restoreWirelessOnFailure(...) = %v, want %v", restored, tc.wantRestored)
			}
			if got := strings.Contains(jobLogText(job), "Rolling back wireless config"); got != tc.wantRestored {
				t.Errorf("rollback log line present = %v, want %v", got, tc.wantRestored)
			}
		})
	}
}

// TestDeployFailureSitesRestoreWireless is a STATIC guard over the real
// deploy.go (via the go:embed in pins_test.go), in the same spirit as
// TestDeployStepIndexGuard: it is the only mechanism that fails when the refusal
// is re-inlined or one of the terminal failure sites stops restoring, because no
// in-process test can drive runDeployment without a router.
func TestDeployFailureSitesRestoreWireless(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "deploy.go", deployGoSrc, 0)
	if err != nil {
		t.Fatalf("parsing deploy.go: %v", err)
	}

	// runDeployment's body, and the guard: its step-6 refusal must be reached
	// through refuseMissingRequestedRelease, i.e. that call is the CONDITION of
	// an if whose body returns. Re-inlining `if !pkgOnRouter && ... { jobFail...;
	// return }` would drop the rollback again — exactly the finding.
	var runDecl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runDeployment" {
			runDecl = fd
		}
	}
	if runDecl == nil || runDecl.Body == nil {
		t.Fatal("deploy.go declares no runDeployment function")
	}
	gateGuardsReturn := false
	ast.Inspect(runDecl, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		call, ok := ifs.Cond.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "refuseMissingRequestedRelease" {
			return true
		}
		if len(ifs.Body.List) == 1 {
			if _, ok := ifs.Body.List[0].(*ast.ReturnStmt); ok {
				gateGuardsReturn = true
			}
		}
		return true
	})
	if !gateGuardsReturn {
		t.Errorf("runDeployment no longer reaches its step-6 refusal through " +
			"`if refuseMissingRequestedRelease(...) { return }` — the refusal path " +
			"must use the shared gate so it cannot skip the wireless rollback")
	}

	runStart := fset.Position(runDecl.Body.Pos()).Offset
	runEnd := fset.Position(runDecl.Body.End()).Offset
	runBody := deployGoSrc[runStart:runEnd]

	if strings.Contains(runBody, "rollbackWireless(") {
		t.Errorf("runDeployment calls rollbackWireless directly — every terminal failure " +
			"after step 5 must go through restoreWirelessOnFailure so the staCommitted " +
			"decision (and the log that states what happened) cannot be skipped")
	}
	if got := strings.Count(runBody, "restoreWirelessOnFailure("); got < 2 {
		t.Errorf("runDeployment calls restoreWirelessOnFailure %d time(s), want >= 2 "+
			"(the step-6 feed-install failure and the step-11 health failure)", got)
	}

	refuse := funcBodyText(t, fset, f, "refuseMissingRequestedRelease")
	if !strings.Contains(refuse, "restoreWirelessOnFailure(") {
		t.Error("refuseMissingRequestedRelease does not restore the wireless snapshot before failing")
	}
	if !strings.Contains(refuse, "jobFail(") {
		t.Error("refuseMissingRequestedRelease no longer fails the job")
	}

	restore := funcBodyText(t, fset, f, "restoreWirelessOnFailure")
	if !strings.Contains(restore, "if !staCommitted") {
		t.Error("restoreWirelessOnFailure no longer gates the rollback on staCommitted")
	}
	if !strings.Contains(restore, "wirelessRollback(") {
		t.Error("restoreWirelessOnFailure does not call the wirelessRollback seam")
	}

	// The seam must delegate to the real rollback, so the behaviour these tests
	// observe cannot drift from what a deploy does.
	if !strings.Contains(deployGoSrc, "var wirelessRollback = func(client *ssh.Client) { rollbackWireless(client) }") {
		t.Error("the wirelessRollback seam no longer delegates to rollbackWireless — the unit tests would then pin behaviour the deploy does not have")
	}
}

// funcBodyText returns the body source of the named function declared in
// deploy.go, or "" when no such function exists.
func funcBodyText(t *testing.T, fset *token.FileSet, f *ast.File, name string) string {
	t.Helper()
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name || fd.Body == nil {
			continue
		}
		return deployGoSrc[fset.Position(fd.Body.Pos()).Offset:fset.Position(fd.Body.End()).Offset]
	}
	t.Errorf("deploy.go declares no function %s", name)
	return ""
}
