package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
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

// ─── post-step-5 terminal failure sites ──────────────────────────────────────
//
// postStep5FailureSites is the PINNED REGISTRY of every terminal failure in
// runDeployment that runs after step 5 committed the STA config — i.e. every
// "return after jobFail" that owes the operator a restored wireless config.
//
// The guard below (TestDeployFailureSitesRestoreWireless) asserts the registry and
// the file agree in BOTH directions, so:
//
//   - a site that stops restoring fails the test (it is no longer a
//     jobFailAfterRestore call), and
//   - a NEW terminal failure after step 5 cannot be added without registering it
//     here — which is the moment to decide whether it restores.
//
// The detail strings are the stepDetail literals as written at the call site (the
// leading literal of a concatenation); "what" is the operator-visible failure.
//
// This replaces the earlier `strings.Count(runBody, "restoreWirelessOnFailure(")
// >= 2` check, whose doc comment read broader than it delivered: a count is not
// per-site coverage, and it stayed green while seven post-step-5 sites returned
// without restoring.
var postStep5FailureSites = []failureSite{
	{6, "Could not determine router CPU architecture", "undetectable CPU arch"},
	{6, "Unsupported CPU arch ", "arch whose package URL cannot be built (defensive: selectPkgURL is generic today)"},
	{6, "opkg refused to downgrade tollgate-wrt", "opkg keeps the previous package"},
	{6, "tollgate-wrt install failed (apk error)", "apk reports an install/upgrade failure"},
	{6, "tollgate-wrt version mismatch", "installed version is not the requested release"},
	{6, "tollgate-wrt install failed", "feed last resort: no package on the router"},
	{8, "captive portal assets missing", "dead-portal regression: splash.html references uninstalled bundles"},
	{10, "tollgate-wrt not installed", "package init script missing at step 10"},
	{11, "tollgate API up but no advertisement", "health: API up, no pricing advertisement"},
	{11, "tollgate service not listening on :2121", "health: service not listening"},
}

// failureSite is one terminal-failure call site: the deploy step it fails and the
// leading literal of its stepDetail.
type failureSite struct {
	step   int
	detail string
	what   string
}

func (s failureSite) key() string { return fmt.Sprintf("step %d %q", s.step, s.detail) }

// failureSitesIn classifies every jobFail / jobFailAfterRestore call inside fn:
// restoring for the ones that go through the shared exit, bare for the ones that
// do not.
func failureSitesIn(fn *ast.FuncDecl) (restoring, bare []failureSite) {
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch id.Name {
		case "jobFailAfterRestore":
			// jobFailAfterRestore(job, client, staCommitted, step, stepDetail, jobErr)
			if len(call.Args) < 5 {
				return true
			}
			if step, ok := intLiteral(call.Args[3]); ok {
				restoring = append(restoring, failureSite{step: step, detail: firstStringLiteral(call.Args[4])})
			}
		case "jobFail":
			// jobFail(job, step, stepDetail, jobErr)
			if len(call.Args) < 3 {
				return true
			}
			if step, ok := intLiteral(call.Args[1]); ok {
				bare = append(bare, failureSite{step: step, detail: firstStringLiteral(call.Args[2])})
			}
		}
		return true
	})
	return restoring, bare
}

// intLiteral returns the value of an integer literal expression.
func intLiteral(e ast.Expr) (int, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	n, err := strconv.Atoi(lit.Value)
	if err != nil {
		return 0, false
	}
	return n, true
}

// firstStringLiteral returns the leftmost string literal in an expression, which
// for a concatenated stepDetail is its informative prefix
// ("Unsupported CPU arch " + routerArch → "Unsupported CPU arch ").
func firstStringLiteral(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if s, err := strconv.Unquote(x.Value); err == nil {
				return s
			}
		}
	case *ast.BinaryExpr:
		return firstStringLiteral(x.X)
	case *ast.ParenExpr:
		return firstStringLiteral(x.X)
	}
	return ""
}

// deployFuncDecl returns the named function declaration from deploy.go.
func deployFuncDecl(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("deploy.go declares no function %s", name)
	return nil
}

// TestDeployFailureSitesRestoreWireless is a STATIC guard over the real
// deploy.go (via the go:embed in pins_test.go), in the same spirit as
// TestDeployStepIndexGuard: it is the only mechanism that fails when the refusal
// is re-inlined, or when ONE of the post-step-5 terminal failure sites stops
// restoring, because no in-process test can drive runDeployment without a router.
//
// It pins per-site coverage, not a call count:
//
//  1. no `jobFail` call with a literal step >= 6 may remain in runDeployment —
//     those are exactly the sites that owe the restore;
//  2. the jobFailAfterRestore sites must be EXACTLY postStep5FailureSites, so a
//     removed site and an unregistered new one both fail;
//  3. the step-5 sites inside configureSTA (which cannot use the staCommitted
//     gate, because runDeployment sets it only after configureSTA returns) must
//     restore through restoreWirelessFromSTACommit, and attemptSTA must report the
//     one case that cannot be restored.
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
	runDecl := deployFuncDecl(t, f, "runDeployment")
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
			"after step 5 must go through jobFailAfterRestore/restoreWirelessOnFailure so the " +
			"staCommitted decision (and the log that states what happened) cannot be skipped")
	}

	// 1 & 2 — per-site coverage of the post-step-5 terminal failures.
	restoring, bare := failureSitesIn(runDecl)
	for _, s := range bare {
		if s.step >= 6 {
			t.Errorf("runDeployment fails %s with a BARE jobFail — every terminal failure "+
				"after step 5 must go through jobFailAfterRestore (which restores the pre-deploy "+
				"wireless snapshot before failing); if it genuinely must not restore, say why in "+
				"the registry comment instead of leaving it bare", s.key())
		}
	}
	got := map[string]int{}
	for _, s := range restoring {
		got[s.key()]++
	}
	want := map[string]int{}
	for _, s := range postStep5FailureSites {
		want[s.key()]++
	}
	for _, s := range postStep5FailureSites {
		switch got[s.key()] {
		case 1: // exactly one site, as registered
		case 0:
			t.Errorf("registered post-step-5 failure site %s (%s) no longer restores — "+
				"it must call jobFailAfterRestore", s.key(), s.what)
		default:
			t.Errorf("registered post-step-5 failure site %s appears %d times in runDeployment, want 1",
				s.key(), got[s.key()])
		}
	}
	for _, s := range restoring {
		if want[s.key()] == 0 {
			t.Errorf("runDeployment has a post-step-5 terminal failure site %s that is not in "+
				"postStep5FailureSites — register it (and confirm it restores) so the site cannot "+
				"be added without the decision being made", s.key())
		}
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

	// The shared exit: restore BEFORE failing, so a failing deploy is never
	// observed as finished while the radios are still committed, and the error
	// says which of the two happened.
	shared := funcBodyText(t, fset, f, "jobFailAfterRestore")
	restoreAt := strings.Index(shared, "restoreWirelessOnFailure(")
	failAt := strings.Index(shared, "jobFail(")
	if restoreAt < 0 || failAt < 0 || restoreAt > failAt {
		t.Errorf("jobFailAfterRestore must call restoreWirelessOnFailure(...) BEFORE jobFail(...):\n%s", shared)
	}
	if !strings.Contains(shared, "pre-deploy wireless config restored") {
		t.Error("jobFailAfterRestore no longer states the restore in the operator-facing error")
	}

	// The step-5 wrapper must be the same decision, not a second one.
	fromCommit := funcBodyText(t, fset, f, "restoreWirelessFromSTACommit")
	if !strings.Contains(fromCommit, "restoreWirelessOnFailure(") {
		t.Error("restoreWirelessFromSTACommit no longer delegates to restoreWirelessOnFailure — " +
			"the step-5 sites would then make their own rollback decision")
	}

	// The seam must delegate to the real rollback, so the behaviour these tests
	// observe cannot drift from what a deploy does.
	if !strings.Contains(deployGoSrc, "var wirelessRollback = func(client *ssh.Client) { rollbackWireless(client) }") {
		t.Error("the wirelessRollback seam no longer delegates to rollbackWireless — the unit tests would then pin behaviour the deploy does not have")
	}

	// The restore must cover BOTH things the step-5 commit changed: the wireless
	// config is restored from its snapshot and network.wwan is put back (deleted
	// when this run created it, else its pre-deploy proto). A restore that only
	// copies /etc/config/wireless leaves an interface pointing at an iface the
	// rollback just removed.
	rollback := funcBodyText(t, fset, f, "rollbackWireless")
	if !strings.Contains(rollback, "rollbackWirelessCmd") {
		t.Error("rollbackWireless no longer runs the shared rollbackWirelessCmd")
	}
	for _, want := range []string{
		"wireless.pre-tollgate", // the wireless snapshot is still restored
		"network.wwan.pre-tollgate",
		"delete network.wwan", // created by this run => deleted
		"network.wwan.proto",  // pre-existing section => its proto is put back
	} {
		if !strings.Contains(rollbackWirelessCmd, want) {
			t.Errorf("rollbackWirelessCmd does not %q — the restore would not undo the network.wwan "+
				"half of the step-5 commit (see docs/deploy-failure-rollback.md)", want)
		}
	}
	// …and the STA script must take that snapshot BEFORE it writes the section.
	sta := staSetupScriptFor("upstream-ssid", "upstream-key", "2.4", "radio0")
	for _, want := range []string{
		"/tmp/network.wwan.pre-tollgate",
		"ABSENT",
		"EXISTED",
		"/tmp/network.wwan.proto.pre-tollgate",
	} {
		if !strings.Contains(sta, want) {
			t.Errorf("staSetupScriptFor no longer records %q, so rollbackWireless cannot put network.wwan back", want)
		}
	}
	if snapAt, setAt := strings.Index(sta, "/tmp/network.wwan.pre-tollgate"), strings.Index(sta, "uci set network.wwan=interface"); snapAt < 0 || setAt < 0 || snapAt > setAt {
		t.Errorf("staSetupScriptFor writes network.wwan BEFORE snapshotting it (snapAt=%d setAt=%d)", snapAt, setAt)
	}

	// Step-5 (inside configureSTA) sites: the deploy cannot use the staCommitted
	// gate there, so they must restore through restoreWirelessFromSTACommit, and
	// attemptSTA must report the commit it could not roll back instead of letting
	// the caller claim "wireless config rolled back".
	//
	// The check is scoped to the INNERMOST if-body that carries the failure (the
	// function also restores on the retry path, so "the function mentions the
	// helper" would be true even with this site's restore removed).
	cfg := deployFuncDecl(t, f, "configureSTA")
	bodyStart, bodyEnd, restoreIn := -1, -1, false
	ast.Inspect(cfg, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		start := fset.Position(ifs.Body.Pos()).Offset
		end := fset.Position(ifs.Body.End()).Offset
		body := deployGoSrc[start:end]
		if !strings.Contains(body, `"upstream has no internet"`) {
			return true
		}
		if bodyStart < 0 || end-start < bodyEnd-bodyStart { // innermost such body
			bodyStart, bodyEnd = start, end
			restoreIn = strings.Contains(body, "restoreWirelessFromSTACommit(")
		}
		return true
	})
	if bodyStart < 0 {
		t.Error("configureSTA no longer fails the deploy when the upstream has no internet")
	} else if !restoreIn {
		t.Errorf("configureSTA fails with \"upstream has no internet\" WITHOUT restoring the STA commit "+
			"(body at offset %d, %d bytes): the association committed the config, and runDeployment's "+
			"staCommitted is still false, so no other gate can fire there", bodyStart, bodyEnd-bodyStart)
	}
	if !strings.Contains(funcBodyText(t, fset, f, "configureSTA"), "could NOT be rolled back") {
		t.Error("configureSTA no longer states the one failure it cannot clean up (a committed STA config the " +
			"router stopped answering SSH for) — the operator would be told the wireless config was rolled back")
	}
	attempt := funcBodyText(t, fset, f, "attemptSTA")
	if !strings.Contains(attempt, "return nil, false, true") {
		t.Error("attemptSTA no longer reports the committed-but-unreachable case (it must return " +
			"leftCommitted=true instead of a silent false, which the callers would read as \"nothing to restore\")")
	}
}

// TestJobFailAfterRestoreIsTheExitForEveryPostStep5Failure pins the shared exit
// the ten post-step-5 terminal failures now use (card t_4709a091): it restores
// through the same staCommitted gate the rest of the deploy uses, BEFORE marking
// the job failed, and the operator-facing error says which of the two happened —
// never a restore that did not run, never a silent omission of one that did.
func TestJobFailAfterRestoreIsTheExitForEveryPostStep5Failure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		staCommitted bool
		wantCalls    int
		wantStated   bool
	}{
		{"sta committed by this run (STA mode)", true, 1, true},
		{"no sta committed by this run (WAN mode)", false, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := newJob("192.168.1.1")
			// Identity only: the rollback side effect is stubbed. A non-nil fake
			// lets the test prove the live client is handed to the rollback.
			client := new(ssh.Client)
			calls := stubWirelessRollback(t, job)

			jobFailAfterRestore(job, client, tc.staCommitted, 6, "tollgate-wrt version mismatch", "installed 0.5.0, wanted 0.6.0")

			if len(*calls) != tc.wantCalls {
				t.Fatalf("wireless rollback calls = %d, want %d", len(*calls), tc.wantCalls)
			}
			if tc.wantCalls > 0 {
				if got := (*calls)[0].client; got != client {
					t.Errorf("rollback was handed %p, want the live deploy client %p", got, client)
				}
				if got := (*calls)[0].jobStatus; got != "running" {
					t.Errorf("rollback ran while the job was already %q; it must run BEFORE the job is marked failed", got)
				}
			}
			if job.Status != "failed" {
				t.Errorf("job.Status = %q, want \"failed\" — a terminal failure must never leave the job running", job.Status)
			}
			if got := job.Steps[6].Status; got != "failed" {
				t.Errorf("step 6 status = %q, want \"failed\"", got)
			}
			if got := job.Steps[6].Detail; got != "tollgate-wrt version mismatch" {
				t.Errorf("step 6 detail = %q, want the failure detail", got)
			}
			if !strings.Contains(job.Error, "installed 0.5.0, wanted 0.6.0") {
				t.Errorf("job.Error = %q, want it to carry the failure text", job.Error)
			}
			stated := strings.Contains(job.Error, "pre-deploy wireless config restored")
			if stated != tc.wantStated {
				t.Errorf("job.Error states a restore = %v, want %v: %q", stated, tc.wantStated, job.Error)
			}
			rolledBack := strings.Contains(jobLogText(job), "Rolling back wireless config")
			if rolledBack != tc.wantStated {
				t.Errorf("deploy log claims a rollback = %v, want %v", rolledBack, tc.wantStated)
			}
		})
	}
}

// TestRestoreWirelessFromSTACommitIsTheStep5Gate pins the wrapper the step-5
// failure paths inside configureSTA use. There the STA commit has already
// happened (a successful association) while runDeployment's staCommitted is still
// false — it is only set once configureSTA returns true — so the failing path has
// to state the commit itself instead of relying on the gate's flag.
func TestRestoreWirelessFromSTACommitIsTheStep5Gate(t *testing.T) {
	job := newJob("192.168.1.1")
	client := new(ssh.Client)
	calls := stubWirelessRollback(t, job)

	if !restoreWirelessFromSTACommit(job, client) {
		t.Error("restoreWirelessFromSTACommit(...) = false, want true: a committed STA config must be restored")
	}
	if len(*calls) != 1 {
		t.Fatalf("wireless rollback calls = %d, want 1", len(*calls))
	}
	if got := (*calls)[0].client; got != client {
		t.Errorf("rollback was handed %p, want the live client %p", got, client)
	}
	if got := (*calls)[0].jobStatus; got != "running" {
		t.Errorf("rollback ran while the job was %q; it must run before the job is failed", got)
	}
	if !strings.Contains(jobLogText(job), "Rolling back wireless config") {
		t.Error("the step-5 restore does not log the rollback")
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
