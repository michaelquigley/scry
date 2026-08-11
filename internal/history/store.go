// Package history owns scry's append-only, year-segmented transition ledger.
package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/df/dl"
	"github.com/michaelquigley/scry/internal/model"
)

// lineVersion stamps every appended line. a segment accretes across binary
// upgrades, so the line is the unit that must stay individually readable.
const lineVersion = 1

var segmentPattern = regexp.MustCompile(`^\d{4}\.jsonl$`)

// EventType names the three line shapes a segment carries.
type EventType string

const (
	EventTransition EventType = "transition"
	EventStart      EventType = "start"
	EventStop       EventType = "stop"
)

// Valid reports whether type is part of the v1 ledger vocabulary.
func (eventType EventType) Valid() bool {
	switch eventType {
	case EventTransition, EventStart, EventStop:
		return true
	default:
		return false
	}
}

// Event is one ledger line. the transition fields are absent on lifecycle
// lines, which belong to the estate rather than to any check.
type Event struct {
	Version   int         `dd:"v,+required"`
	TS        time.Time   `dd:"ts,+required"`
	Type      EventType   `dd:"event,+required"`
	Check     string      `dd:"check,+omitempty"`
	Kind      model.Kind  `dd:"kind,+omitempty"`
	From      model.State `dd:"from,+omitempty"`
	To        model.State `dd:"to,+omitempty"`
	PrevSince *time.Time  `dd:"prev_since"`
	Detail    *string     `dd:"detail"`
}

func (event Event) validate() error {
	if event.Version != lineVersion {
		return fmt.Errorf("unsupported line version %d", event.Version)
	}
	if event.TS.IsZero() {
		return fmt.Errorf("ts is required")
	}
	if !event.Type.Valid() {
		return fmt.Errorf("invalid event %q", event.Type)
	}
	if event.Type != EventTransition {
		// a flat line cannot distinguish an absent field from an explicitly
		// empty one, so zero is the invariant a lifecycle line claims.
		if event.Check != "" || event.Kind != "" || event.From != "" || event.To != "" || event.PrevSince != nil || event.Detail != nil {
			return fmt.Errorf("%s carries transition fields", event.Type)
		}
		return nil
	}
	if event.Check == "" {
		return fmt.Errorf("transition check is required")
	}
	if !event.Kind.Valid() {
		return fmt.Errorf("check %q: invalid kind %q", event.Check, event.Kind)
	}
	if !event.From.Valid() {
		return fmt.Errorf("check %q: invalid from state %q", event.Check, event.From)
	}
	if !event.To.Valid() {
		return fmt.Errorf("check %q: invalid to state %q", event.Check, event.To)
	}
	if event.From == event.To {
		return fmt.Errorf("check %q: from and to are both %q", event.Check, event.From)
	}
	if event.PrevSince == nil || event.PrevSince.IsZero() {
		return fmt.Errorf("check %q: prev_since is required", event.Check)
	}
	return nil
}

// Tail pairs the state a check occupied at a bound with the instant it began.
type Tail struct {
	State model.State
	Since time.Time
}

// Window is everything the ledger alone can say about one time range. ids are
// absent from the resolution maps when no event can speak for them.
type Window struct {
	Events         []Event
	StateAtFrom    map[string]model.State
	TailAtTo       map[string]Tail
	WatchingAtFrom bool
}

// Store appends to and reads one year-segmented ledger directory. it holds no
// clock and no lock: the engine serializes every append and every read.
type Store struct {
	dir string
}

// NewStore returns a ledger store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Boot prepares the ledger for one daemon run. it parses every segment whole,
// closes an unclean ledger with the stop the dead daemon never wrote, opens
// the run with a start, and warns once for each id the registry no longer
// carries. a segment that does not parse fails the boot.
func (store *Store) Boot(at, lastSaved time.Time, configured map[string]struct{}) error {
	if at.IsZero() {
		return fmt.Errorf("history boot time is required")
	}
	if err := os.MkdirAll(store.dir, 0o755); err != nil {
		return fmt.Errorf("create history directory %q: %w", store.dir, err)
	}
	events, err := store.readAll()
	if err != nil {
		return err
	}
	warnOrphans(events, configured)

	if len(events) > 0 {
		newest := events[len(events)-1]
		if newest.Type != EventStop {
			// saved and the ledger's own last line are both liveness
			// evidence; the later of them marks the last instant anything
			// testified the daemon was alive.
			closedAt := newest.TS
			switch {
			case lastSaved.IsZero():
				dl.Warnf(
					"history ledger has no clean stop and the state file carries no saved stamp; closing at the newest event '%s'",
					newest.TS.Format(time.RFC3339),
				)
			case lastSaved.After(closedAt):
				closedAt = lastSaved
			}
			if err := store.AppendStop(closedAt); err != nil {
				return err
			}
		}
	}
	return store.AppendStart(at)
}

// AppendTransition records one state change, detail omitted when empty.
func (store *Store) AppendTransition(transition model.Transition) error {
	previousSince := transition.PreviousSince
	event := Event{
		Version:   lineVersion,
		TS:        transition.At,
		Type:      EventTransition,
		Check:     transition.CheckID,
		Kind:      transition.Kind,
		From:      transition.From,
		To:        transition.To,
		PrevSince: &previousSince,
	}
	if transition.Result != nil && transition.Result.Detail != "" {
		detail := transition.Result.Detail
		event.Detail = &detail
	}
	return store.append(event)
}

// AppendStart opens a watched span at at.
func (store *Store) AppendStart(at time.Time) error {
	return store.append(Event{Version: lineVersion, TS: at, Type: EventStart})
}

// AppendStop closes the watched span at at.
func (store *Store) AppendStop(at time.Time) error {
	return store.append(Event{Version: lineVersion, TS: at, Type: EventStop})
}

// Window resolves both bounds and collects every event inside [from, to].
func (store *Store) Window(from, to time.Time) (Window, error) {
	events, err := store.readAll()
	if err != nil {
		return Window{}, err
	}

	window := Window{
		Events:      make([]Event, 0),
		StateAtFrom: make(map[string]model.State),
		TailAtTo:    make(map[string]Tail),
	}
	atFrom := newResolver(from)
	atTo := newResolver(to)
	for _, event := range events {
		if event.Type == EventTransition {
			atFrom.observe(event)
			atTo.observe(event)
		} else if !event.TS.After(from) {
			window.WatchingAtFrom = event.Type == EventStart
		}
		if !event.TS.Before(from) && !event.TS.After(to) {
			window.Events = append(window.Events, event)
		}
	}
	for id, tail := range atFrom.resolve() {
		window.StateAtFrom[id] = tail.State
	}
	window.TailAtTo = atTo.resolve()
	return window, nil
}

// Prune removes every transition event recorded under name; the estate-scoped
// lifecycle events are untouched. it is an offline operator command run
// against a stopped daemon, and it strict-reads the whole ledger before
// touching any of it — curation for a healthy ledger, never a corruption
// remedy.
func (store *Store) Prune(name string) (int, error) {
	if name == "" {
		return 0, fmt.Errorf("check name is required")
	}
	names, err := store.segments()
	if err != nil {
		return 0, err
	}
	parsed := make([][]Event, len(names))
	for i, segment := range names {
		events, err := readSegment(filepath.Join(store.dir, segment))
		if err != nil {
			return 0, err
		}
		parsed[i] = events
	}

	removed := 0
	for i, segment := range names {
		kept := make([]Event, 0, len(parsed[i]))
		for _, event := range parsed[i] {
			if event.Type == EventTransition && event.Check == name {
				removed++
				continue
			}
			kept = append(kept, event)
		}
		if len(kept) == len(parsed[i]) {
			continue
		}
		path := filepath.Join(store.dir, segment)
		if len(kept) == 0 {
			if err := os.Remove(path); err != nil {
				return 0, fmt.Errorf("remove history segment %q: %w", path, err)
			}
			if err := syncDir(store.dir); err != nil {
				return 0, err
			}
			continue
		}
		if err := store.rewrite(path, kept); err != nil {
			return 0, err
		}
	}
	return removed, nil
}

// resolver applies both bound rules to one instant as events stream past in
// append order.
type resolver struct {
	bound  time.Time
	latest map[string]Tail
	after  map[string]Event
}

func newResolver(bound time.Time) *resolver {
	return &resolver{
		bound:  bound,
		latest: make(map[string]Tail),
		after:  make(map[string]Event),
	}
}

func (resolution *resolver) observe(event Event) {
	if !event.TS.After(resolution.bound) {
		resolution.latest[event.Check] = Tail{State: event.To, Since: event.TS}
		return
	}
	if _, found := resolution.after[event.Check]; !found {
		resolution.after[event.Check] = event
	}
}

// resolve names the state at the bound for every check the ledger can speak
// for: the newest transition at or before it carries its own to-state from
// its own timestamp, and failing that the first transition after it carries
// its from-state — a backward claim admitted only as far as prev_since, the
// existence boundary no state extends past.
func (resolution *resolver) resolve() map[string]Tail {
	resolved := make(map[string]Tail, len(resolution.latest))
	for id, tail := range resolution.latest {
		resolved[id] = tail
	}
	for id, event := range resolution.after {
		if _, found := resolved[id]; found {
			continue
		}
		if event.PrevSince.After(resolution.bound) {
			continue
		}
		resolved[id] = Tail{State: event.From, Since: *event.PrevSince}
	}
	return resolved
}

func warnOrphans(events []Event, configured map[string]struct{}) {
	orphans := make([]string, 0)
	seen := make(map[string]struct{})
	for _, event := range events {
		if event.Type != EventTransition {
			continue
		}
		if _, found := seen[event.Check]; found {
			continue
		}
		seen[event.Check] = struct{}{}
		if _, found := configured[event.Check]; !found {
			orphans = append(orphans, event.Check)
		}
	}
	sort.Strings(orphans)
	for _, id := range orphans {
		dl.Warnf("ignoring history for unconfigured check '%s'; 'scry prune %s' removes it", id, id)
	}
}

// append lands one complete line in the segment named by its own year. the
// descriptor is opened per append rather than cached, which costs nothing at
// this volume and makes year rollover need no special case at all.
func (store *Store) append(event Event) error {
	if err := event.validate(); err != nil {
		return fmt.Errorf("append history event: %w", err)
	}
	line, err := marshalEvent(event)
	if err != nil {
		return err
	}

	path := store.segmentPath(event.TS.Year())
	created := false
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat history segment %q: %w", path, err)
		}
		created = true
	}

	segment, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open history segment %q: %w", path, err)
	}
	defer segment.Close()
	if _, err := segment.Write(line); err != nil {
		return fmt.Errorf("write history segment %q: %w", path, err)
	}
	if err := segment.Sync(); err != nil {
		return fmt.Errorf("sync history segment %q: %w", path, err)
	}
	if err := segment.Close(); err != nil {
		return fmt.Errorf("close history segment %q: %w", path, err)
	}
	if created {
		return syncDir(store.dir)
	}
	return nil
}

// rewrite replaces one whole segment through the state file's atomic
// discipline: temporary file, chmod, write, sync, rename, directory sync.
func (store *Store) rewrite(path string, events []Event) error {
	data := make([]byte, 0, len(events)*128)
	for _, event := range events {
		line, err := marshalEvent(event)
		if err != nil {
			return err
		}
		data = append(data, line...)
	}

	temp, err := os.CreateTemp(store.dir, ".scry-history-*")
	if err != nil {
		return fmt.Errorf("create history temporary file in %q: %w", store.dir, err)
	}
	tempName := temp.Name()
	renamed := false
	defer func() {
		_ = temp.Close()
		if !renamed {
			_ = os.Remove(tempName)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("set history temporary file permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write history temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync history temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close history temporary file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace history segment %q: %w", path, err)
	}
	renamed = true
	return syncDir(store.dir)
}

func (store *Store) segmentPath(year int) string {
	return filepath.Join(store.dir, fmt.Sprintf("%04d.jsonl", year))
}

// segments returns every segment file name in ascending year order. anything
// else in the directory — a temporary file left by an interrupted rewrite —
// is not a segment and is not read.
func (store *Store) segments() ([]string, error) {
	entries, err := os.ReadDir(store.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history directory %q: %w", store.dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !segmentPattern.MatchString(entry.Name()) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// readAll returns the whole ledger in append order: segments in name order,
// lines in file order.
func (store *Store) readAll() ([]Event, error) {
	names, err := store.segments()
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0)
	for _, name := range names {
		segment, err := readSegment(filepath.Join(store.dir, name))
		if err != nil {
			return nil, err
		}
		events = append(events, segment...)
	}
	return events, nil
}

// readSegment strict-binds and validates every line of one segment, in file
// order. one bad line fails the whole read, naming the file and the line.
func readSegment(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read history segment %q: %w", path, err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")
	events := make([]Event, 0, len(lines))
	for i, line := range lines {
		var event Event
		if err := dd.BindJSON(&event, []byte(line), dd.Strict()); err != nil {
			return nil, fmt.Errorf("parse history segment %q line %d: %w", path, i+1, err)
		}
		if err := event.validate(); err != nil {
			return nil, fmt.Errorf("history segment %q line %d: %w", path, i+1, err)
		}
		events = append(events, event)
	}
	return events, nil
}

// marshalEvent renders one complete line. dd owns both the field mapping and
// the serialization; the indented document it produces is then compacted,
// because the ledger's unit is a line rather than a file.
func marshalEvent(event Event) ([]byte, error) {
	document, err := dd.UnbindJSON(event)
	if err != nil {
		return nil, fmt.Errorf("encode history event: %w", err)
	}
	var line bytes.Buffer
	if err := json.Compact(&line, document); err != nil {
		return nil, fmt.Errorf("compact history event: %w", err)
	}
	line.WriteByte('\n')
	return line.Bytes(), nil
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open history directory %q for sync: %w", dir, err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync history directory %q: %w", dir, err)
	}
	return nil
}
