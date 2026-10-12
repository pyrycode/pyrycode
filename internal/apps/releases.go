package apps

import (
	"path/filepath"
	"strings"
)

// Release identifies an immutable build within its containing local registration.
// Recorded versions and digests do not prove a successful build or readiness.
type Release struct {
	ReleaseVersion string `json:"release_version"`
	BuildID        string `json:"build_id"`
	BuildRoot      string `json:"build_root"`
	NodeVersion    string `json:"node_version"`
	NPMVersion     string `json:"npm_version"`
	LockfileDigest string `json:"lockfile_digest"`
}

// ReleaseReference is app-scoped; legacy releases need no history entry.
type ReleaseReference struct {
	ServerID       string `json:"server_id"`
	AppID          string `json:"app_id"`
	ReleaseVersion string `json:"release_version"`
}

// BackupReference locates a publisher-verified snapshot, without verifying it.
type BackupReference struct {
	ServerID  string `json:"server_id"`
	AppID     string `json:"app_id"`
	PublishID string `json:"publish_id"`
	Root      string `json:"root"`
}

// PublishJournal records recovery's durable choice. Before activation commit,
// restore previous data/release (Previous is absent on first publish); after it,
// use Candidate. Storage proves neither verification, health nor restoration.
type PublishJournal struct {
	Candidate           ReleaseReference  `json:"candidate"`
	Previous            *ReleaseReference `json:"previous,omitempty"`
	VerifiedBackup      BackupReference   `json:"verified_backup"`
	ActivationCommitted bool              `json:"activation_committed"`
}

// RecordRelease appends an immutable release version. Identical repeats are no-ops.
func (r *Registry) RecordRelease(serverID, appID string, release Release) (bool, error) {
	return r.change("lifecycle", appID, nil, &lifecycleUpdate{serverID: serverID, metadata: func(record *Record) error {
		for _, prior := range record.ReleaseHistory {
			if prior.ReleaseVersion == release.ReleaseVersion {
				if prior != release {
					return ErrConflict
				}
				return nil
			}
		}
		record.ReleaseHistory = append(record.ReleaseHistory, release)
		return nil
	}})
}

// UpdatePublish replaces or clears a journal, optionally committing lifecycle
// fields atomically. Activation callers supply candidate active/title/state and
// cleared pending/error; recovery callers supply the resulting lifecycle. A new
// publish ID requires clearing the outstanding journal first. Inputs are detached.
func (r *Registry) UpdatePublish(serverID, appID string, journal *PublishJournal, fields *Lifecycle) (bool, error) {
	return r.change("lifecycle", appID, nil, &lifecycleUpdate{serverID: serverID, metadata: func(record *Record) error {
		if journal != nil && record.PublishJournal != nil && journal.VerifiedBackup.PublishID != record.PublishJournal.VerifiedBackup.PublishID {
			return ErrConflict
		}
		record.PublishJournal = journal
		if fields != nil {
			applyLifecycle(record, *fields)
		}
		return nil
	}})
}

func validDigest(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }

func validMetadata(root, serverID string, record Record) bool {
	seen := make(map[string]bool)
	for _, release := range record.ReleaseHistory {
		if !validRelease(release.ReleaseVersion) || seen[release.ReleaseVersion] || !validDigest(release.BuildID) || !validDigest(release.LockfileDigest) || !validRelease(release.NodeVersion) || !validRelease(release.NPMVersion) || release.BuildRoot != filepath.Join(root, record.AppID, "builds", release.BuildID) {
			return false
		}
		seen[release.ReleaseVersion] = true
	}
	j := record.PublishJournal
	if j == nil {
		return true
	}
	validRef := func(ref ReleaseReference) bool {
		return ref.ServerID == serverID && ref.AppID == record.AppID && validRelease(ref.ReleaseVersion)
	}
	b := j.VerifiedBackup
	return validRef(j.Candidate) && (j.Previous == nil || validRef(*j.Previous)) && b.ServerID == serverID && b.AppID == record.AppID && validID(b.PublishID) && b.Root == filepath.Join(root, record.AppID, "backups", b.PublishID)
}

func validMetadataSchema(record map[string]any) bool {
	if history, exists := record["release_history"]; exists {
		releases, ok := history.([]any)
		if !ok {
			return false
		}
		for _, release := range releases {
			if _, ok := storageObject(release, []string{"release_version", "build_id", "build_root", "node_version", "npm_version", "lockfile_digest"}); !ok {
				return false
			}
		}
	}
	if value, exists := record["publish_journal"]; exists {
		journal, ok := storageObject(value, []string{"candidate", "verified_backup", "activation_committed"}, "previous")
		if !ok {
			return false
		}
		for _, key := range []string{"candidate", "previous"} {
			if ref, exists := journal[key]; exists {
				if _, ok := storageObject(ref, []string{"server_id", "app_id", "release_version"}); !ok {
					return false
				}
			}
		}
		if _, ok := storageObject(journal["verified_backup"], []string{"server_id", "app_id", "publish_id", "root"}); !ok {
			return false
		}
	}
	return true
}
