// Package state owns scry's versioned, atomic JSON state file.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/michaelquigley/df/dd"
	"github.com/michaelquigley/scry/internal/model"
)

const fileVersion = 1

// Entry pairs the persisted check kind with its runtime record.
type Entry struct {
	Kind   model.Kind
	Record model.Record
}

// Snapshot is the complete persisted state keyed by check id.
type Snapshot map[string]Entry

// Clone returns a deep copy safe for mutation by another owner.
func (snapshot Snapshot) Clone() Snapshot {
	clone := make(Snapshot, len(snapshot))
	for id, entry := range snapshot {
		entry.Record = entry.Record.Clone()
		clone[id] = entry
	}
	return clone
}

// Store loads and atomically replaces one state file.
type Store struct {
	path string
}

// NewStore returns a state store rooted at path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Load reads the whole state file. a missing file is an empty first boot.
func (store *Store) Load() (Snapshot, error) {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state file %q: %w", store.path, err)
	}

	var disk diskFile
	if err := dd.BindJSON(&disk, data, dd.Strict()); err != nil {
		return nil, fmt.Errorf("parse state file %q: %w", store.path, err)
	}
	if disk.Version != fileVersion {
		return nil, fmt.Errorf("state file %q: unsupported version %d", store.path, disk.Version)
	}

	snapshot := make(Snapshot, len(disk.Checks))
	for id, persisted := range disk.Checks {
		if err := persisted.validate(id); err != nil {
			return nil, fmt.Errorf("state file %q: %w", store.path, err)
		}
		entry := persisted.entry()
		if err := validateEntry(id, entry); err != nil {
			return nil, fmt.Errorf("state file %q: %w", store.path, err)
		}
		snapshot[id] = entry
	}
	return snapshot, nil
}

// Save writes the whole snapshot through a same-directory temporary file.
func (store *Store) Save(snapshot Snapshot) error {
	for id, entry := range snapshot {
		if err := validateEntry(id, entry); err != nil {
			return fmt.Errorf("write state file %q: %w", store.path, err)
		}
	}

	disk := diskFile{
		Version: fileVersion,
		Checks:  make(map[string]diskRecord, len(snapshot)),
	}
	for id, entry := range snapshot {
		disk.Checks[id] = newDiskRecord(entry)
	}
	data, err := dd.UnbindJSON(disk)
	if err != nil {
		return fmt.Errorf("encode state file %q: %w", store.path, err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(store.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state directory %q: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".scry-state-*")
	if err != nil {
		return fmt.Errorf("create state temporary file in %q: %w", dir, err)
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
		return fmt.Errorf("set state temporary file permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write state temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync state temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close state temporary file: %w", err)
	}
	if err := os.Rename(tempName, store.path); err != nil {
		return fmt.Errorf("replace state file %q: %w", store.path, err)
	}
	renamed = true

	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open state directory %q for sync: %w", dir, err)
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("sync state directory %q: %w", dir, err)
	}
	return nil
}

type diskFile struct {
	Version int                   `dd:"v,+required"`
	Checks  map[string]diskRecord `dd:"checks,+required"`
}

type diskRecord struct {
	Kind             model.Kind    `dd:"kind,+required"`
	State            model.State   `dd:"state,+required"`
	Since            time.Time     `dd:"since,+required"`
	LastSeen         *time.Time    `dd:"last_seen"`
	LastStatus       *model.Status `dd:"last_status"`
	LastDetail       *string       `dd:"last_detail"`
	ConsecutiveFails int           `dd:"consecutive_fails,+required"`
	LastTransition   *time.Time    `dd:"last_transition"`
}

func newDiskRecord(entry Entry) diskRecord {
	persisted := diskRecord{
		Kind:             entry.Kind,
		State:            entry.Record.State,
		Since:            entry.Record.Since,
		LastSeen:         cloneTime(entry.Record.LastSeen),
		ConsecutiveFails: entry.Record.ConsecutiveFails,
		LastTransition:   cloneTime(entry.Record.LastTransition),
	}
	if entry.Record.LastResult != nil {
		status := entry.Record.LastResult.Status
		detail := entry.Record.LastResult.Detail
		persisted.LastStatus = &status
		persisted.LastDetail = &detail
	}
	return persisted
}

func (persisted diskRecord) entry() Entry {
	record := model.Record{
		State:            persisted.State,
		Since:            persisted.Since,
		LastSeen:         cloneTime(persisted.LastSeen),
		ConsecutiveFails: persisted.ConsecutiveFails,
		LastTransition:   cloneTime(persisted.LastTransition),
	}
	if persisted.LastStatus != nil && persisted.LastDetail != nil {
		record.LastResult = &model.Result{
			Status: *persisted.LastStatus,
			Detail: *persisted.LastDetail,
		}
	}
	return Entry{Kind: persisted.Kind, Record: record}
}

func (persisted diskRecord) validate(id string) error {
	if (persisted.LastStatus == nil) != (persisted.LastDetail == nil) {
		return fmt.Errorf("check %q: last_status and last_detail must appear together", id)
	}
	if persisted.LastStatus != nil && !persisted.LastStatus.Valid() {
		return fmt.Errorf("check %q: invalid last_status %q", id, *persisted.LastStatus)
	}
	return nil
}

func validateEntry(id string, entry Entry) error {
	if id == "" {
		return fmt.Errorf("empty check id")
	}
	if !entry.Kind.Valid() {
		return fmt.Errorf("check %q: invalid kind %q", id, entry.Kind)
	}
	record := entry.Record
	if !record.State.Valid() {
		return fmt.Errorf("check %q: invalid state %q", id, record.State)
	}
	if record.Since.IsZero() {
		return fmt.Errorf("check %q: since is required", id)
	}
	if record.LastTransition != nil {
		if record.LastTransition.IsZero() {
			return fmt.Errorf("check %q: last_transition is zero", id)
		}
		if !record.LastTransition.Equal(record.Since) {
			return fmt.Errorf("check %q: last_transition and since disagree", id)
		}
	}
	if record.ConsecutiveFails < 0 {
		return fmt.Errorf("check %q: consecutive_fails is negative", id)
	}
	if record.LastResult != nil && !record.LastResult.Status.Valid() {
		return fmt.Errorf("check %q: invalid last_status %q", id, record.LastResult.Status)
	}

	if entry.Kind == model.KindPassive {
		if record.LastSeen == nil || record.LastSeen.IsZero() {
			return fmt.Errorf("check %q: passive last_seen is required", id)
		}
		if record.ConsecutiveFails != 0 {
			return fmt.Errorf("check %q: passive consecutive_fails must be zero", id)
		}
	} else if record.LastSeen != nil {
		return fmt.Errorf("check %q: active last_seen must be null", id)
	}
	return nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
