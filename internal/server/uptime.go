package server

import (
	"time"

	"github.com/michaelquigley/scry/internal/api"
)

// describeUptime counts one check's time in each state across the document's
// window, on the same terms the dashboard draws its strip: the document's
// resolved bounds and events decide where every state claim stands, and the
// daemon's own lifecycle decides where none may. time the daemon was not
// watching leaves the count entirely, as does time before the check existed.
//
// the walk is the page's (buildStrip in ui/src/history.ts) stated once more in
// Go, and the two are pinned to each other by a shared fixture document under
// internal/api/specs/fixtures. it runs over whole-second instants because that
// is what the wire carries: a consumer recomputing from the document must
// arrive at the same figures.
func describeUptime(document *api.History, entry api.CheckHistory) api.Uptime {
	from := seconds(document.From)
	to := seconds(document.To)
	gaps := unwatchedSpans(document, from, to)

	var uptime api.Uptime
	for _, band := range stateBands(entry, from, to) {
		remaining := []span{band.span}
		for _, gap := range gaps {
			var kept []span
			for _, piece := range remaining {
				kept = append(kept, subtract(piece, gap)...)
			}
			remaining = kept
		}
		for _, piece := range remaining {
			counted := piece.clip(from, to)
			if counted <= 0 {
				continue
			}
			uptime.Watched += counted
			switch band.state {
			case api.StateOk:
				uptime.Ok += counted
			case api.StateLate:
				uptime.Late += counted
			case api.StateFailed:
				uptime.Failed += counted
			}
		}
	}
	return uptime
}

type span struct {
	start, end int64
}

type stateBand struct {
	state api.State
	span
}

// clip measures the part of the span inside the window; zero or negative when
// none of it is.
func (piece span) clip(from, to int64) int64 {
	start := max(piece.start, from)
	end := min(piece.end, to)
	return end - start
}

// stateBands walks the check from the window's start, splitting at each event.
// a check with no state at the start has none until the first event's
// prev_since — the boundary the state being left began at, which is as far
// back as any claim may reach. blank time is simply not a band.
func stateBands(entry api.CheckHistory, from, to int64) []stateBand {
	var bands []stateBand
	cursor := from
	current, claimed := entry.StateAtFrom.Get()

	for _, event := range entry.Events {
		at := seconds(event.Ts)
		if !claimed {
			born := max(cursor, seconds(event.PrevSince))
			bands = append(bands, stateBand{state: event.From, span: span{start: born, end: at}})
		} else {
			bands = append(bands, stateBand{state: current, span: span{start: cursor, end: at}})
		}
		cursor = at
		current, claimed = event.To, true
	}

	// the tail is the document's own pair, which subsumes the last event's floor
	// and is also what gives an eventless mid-window registration its band.
	state, hasState := entry.StateAtTo.Get()
	since, hasSince := entry.Since.Get()
	if hasState && hasSince {
		start := max(cursor, seconds(since))
		bands = append(bands, stateBand{state: state, span: span{start: start, end: to}})
	}
	return bands
}

// unwatchedSpans derives where no state may be claimed: the document's own
// opening flag toggled through its lifecycle events, running to the window's
// end when the last word was a stop.
func unwatchedSpans(document *api.History, from, to int64) []span {
	var spans []span
	watching := document.WatchingAtFrom
	cursor := from
	for _, event := range document.Daemon {
		at := seconds(event.Ts)
		if event.Event == api.LifecycleEventEventStop && watching {
			cursor = at
			watching = false
		} else if event.Event == api.LifecycleEventEventStart && !watching {
			spans = append(spans, span{start: cursor, end: at})
			watching = true
		}
	}
	if !watching {
		spans = append(spans, span{start: cursor, end: to})
	}
	return spans
}

// subtract removes a gap from one span; the gap always wins, because where the
// daemon cannot testify the count claims nothing.
func subtract(piece span, gap span) []span {
	if gap.end <= piece.start || gap.start >= piece.end {
		return []span{piece}
	}
	var remaining []span
	if gap.start > piece.start {
		remaining = append(remaining, span{start: piece.start, end: gap.start})
	}
	if gap.end < piece.end {
		remaining = append(remaining, span{start: gap.end, end: piece.end})
	}
	return remaining
}

// seconds is the instant as the wire carries it: RFC 3339 without a fractional
// part, so the arithmetic truncates exactly as the encoder does.
func seconds(at time.Time) int64 {
	return at.Truncate(time.Second).Unix()
}
