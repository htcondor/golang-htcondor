package jobwatch

import "github.com/PelicanPlatform/classad/classad"

// Deciding whether ONE ad satisfies an event.
//
// This was the body of two switch statements inside Fold -- one for an ad in
// the queue, one for a terminal record -- and it is exported because the
// webhook evaluator has to answer the same question per job. A hook delivers
// a POST for each job that reached a state, so it cannot use Fold's answer:
// Fold summarizes a selection into one verdict and names at most MaxMatched
// of the jobs behind it, deliberately, because a watch over a 100k-job
// cluster has to summarize rather than hand back the cluster.
//
// Two implementations of this would be worse than either. The rules are not
// obvious -- "running" is resolved from a terminal ad by asking whether the
// job ever started, because a short job is never sampled while it runs -- and
// a second copy would be the one that falls behind.

// Observation says where an ad was seen, which is what decides how an event
// resolves from it.
type Observation int

const (
	// InQueue is an ad read from the queue in a non-terminal state. Use
	// TerminalAd to tell the two apart; an ad in the queue with a terminal
	// status is AsTerminal, because HTCondor -- notably OSPool access
	// points -- leaves completed jobs in job_queue.log with JobStatus 4
	// rather than archiving them at once.
	InQueue Observation = iota
	// AsTerminal is a terminal record: a history row, a terminal ad still
	// in the queue, or a terminal record off the change stream.
	AsTerminal
)

// TerminalAd reports whether an ad carries a terminal status, whatever read
// it came from.
func TerminalAd(ad *classad.ClassAd) bool { return isTerminalStatus(statusOf(ad)) }

// Satisfies reports whether this ad, observed as obs, satisfies e.
//
// EventCustom is never satisfied here: it is a ClassAd expression that
// belongs to the watch holding it, and a compiled matcher is not something an
// ad alone can answer for. Callers that support custom events evaluate their
// own condition.
//
// EventDone is satisfied by any terminal record and never by a queued one.
// The other half of "done" -- a job that vanished from a complete queue read
// with nothing to explain it -- is absence, not an ad, so it cannot be
// decided here. A caller that needs it has to track which jobs it has seen;
// Fold does, and so does the webhook evaluator.
func Satisfies(e Event, ad *classad.ClassAd, obs Observation) bool {
	if ad == nil {
		return false
	}
	if obs == AsTerminal {
		switch e {
		case EventDone:
			return true
		case EventSucceeded:
			return succeeded(ad)
		case EventFailed:
			return !succeeded(ad)
		case EventRunning:
			// "Running" is a state, and a state is only observable while
			// it lasts. The queue is sampled every few seconds, so a job
			// that starts and finishes between two samples was never seen
			// running -- which is what a short job on an idle pool does
			// every time. The terminal ad still carries the evidence, so
			// ask whether the job ever started rather than whether it is
			// running now.
			return everRan(ad)
		}
		return false
	}
	switch e {
	case EventHeld:
		return statusOf(ad) == statusHeld
	case EventRunning:
		return statusOf(ad) == statusRunning
	}
	return false
}
