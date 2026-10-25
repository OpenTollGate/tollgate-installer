package app

import (
	"fmt"
	"strings"
)

// Wiring for the tester-side transport verdict (card t_27e348b5).
//
// Every site that used to call failTransportLost now calls
// handleTransportLoss with the tester's uplink read from BEFORE the flash. That
// keeps ONE decision — blame the tester's own link, or the router — in one
// place, and keeps failTransportLost (whose text is about the router, and which
// a live run depends on) unchanged for the router-side case.

// rejoinTargets is the pair of SSIDs a dropped tester can join, when the deploy
// knows them. Both are empty before step 7 resolves the device identity.
type rejoinTargets struct {
	Private string
	Captive string
}

// handleTransportLoss ends a deploy whose SSH session died, with the verdict the
// evidence supports:
//
//   - tester-side: the tester's OWN connection changed (see
//     classifyTransportLoss). The job ends NON-fatally through the pending view
//     with a note that says what changed on this machine, which SSID to join,
//     and what to do next. It never says the installation failed, because the
//     router's state is unknown and the only evidence is the tester's own
//     disconnection.
//   - router-side / unknown: today's failure verdict stands (the router really
//     did stop answering), with the tester-side sentence APPENDED when the
//     tester is on wifi, so a wired operator keeps the existing router-side
//     advice verbatim.
//
// installRan tells the note whether anything on the router was changed, so a
// step-0 loss says "nothing was installed" instead of offering a Re-check for
// an install that never happened.
func handleTransportLoss(job *Job, step int, ip string, err error, before testerUplinkState, installRan bool, tg rejoinTargets) {
	if job == nil {
		return
	}
	after := testerUplinkNow()
	verdict := classifyTransportLoss(transportLossInput{
		Before:         before,
		After:          after,
		RouterIP:       ip,
		RouterDialable: routerDialableFn(ip),
	})
	if verdict != transportLossTesterSide {
		failTransportLost(job, step, ip, err)
		// The router-side text is correct here; the tester-side sentence is an
		// ADDITION, and only for a wifi tester (it is empty on ethernet).
		if notice := testerUplinkNotice(after.Kind, after.Iface); notice != "" {
			job.mu.Lock()
			if job.Error != "" {
				job.Error += "\n\n" + notice
			}
			job.mu.Unlock()
		}
		job.addLog("Transport loss verdict: " + verdict + " — the router stopped answering and this machine's uplink did not change.")
		return
	}

	// Tester-side. Try to get the machine back on the air first, so the note can
	// report what actually happened rather than asking the operator to do it.
	// The FIRST non-empty explanation is kept when no attempt is possible: the
	// private SSID is the management network, so its answer is the informative
	// one, and reporting the last fallback's text instead would describe an SSID
	// the operator was never told to join.
	attempted, detail := false, "this machine was not rejoined automatically."
	for _, ssid := range []string{tg.Private, tg.Captive} {
		ok, why := testerWiFiRejoinFn(ssid)
		if ok {
			attempted, detail = true, why
			break
		}
		if strings.TrimSpace(why) != "" && detail == "this machine was not rejoined automatically." {
			detail = why
		}
	}
	note := testerSideRecoveryNote(testerSideNoteInput{
		RouterIP:     ip,
		Before:       before,
		After:        after,
		CaptiveSSID:  tg.Captive,
		PrivateSSID:  tg.Private,
		RejoinTried:  attempted,
		RejoinDetail: detail,
		InstallRan:   installRan,
	})
	title := testerSideHeading()
	if !installRan {
		title = "Your connection dropped before anything was installed"
	}
	job.addLog(fmt.Sprintf("Transport loss verdict: tester-side — this machine's own uplink changed (%s -> %s) and %s does not answer; NOT reporting an install failure.",
		describeUplinkState(before), describeUplinkState(after), ip))
	jobPendingTitled(job, step, "connection lost on this machine, not a router fault", title, note,
		"not reached — this machine lost its own connection to the router")
}

// jobPendingTitled is jobPending with an explicit banner title. The pending view
// is now reached for two different reasons (a slow-starting merchant, and a
// deploy that lost the TESTER's own connection), and its heading must state
// which one: "the merchant is still starting" would be a lie about a deploy that
// never reached the merchant.
func jobPendingTitled(job *Job, step int, stepDetail, title, note string, skippedDetail ...string) {
	jobPending(job, step, stepDetail, note, skippedDetail...)
	if job == nil {
		return
	}
	job.mu.Lock()
	job.NoteTitle = title
	job.mu.Unlock()
}
