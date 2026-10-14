package apps

import (
	"context"
	"fmt"
	"path/filepath"
)

func newerCandidate(a, b string) bool {
	var x, y [3]int
	if n, err := fmt.Sscanf(a, "%d.%d.%d", &x[0], &x[1], &x[2]); n != 3 || err != nil {
		return false
	}
	if n, err := fmt.Sscanf(b, "%d.%d.%d", &y[0], &y[1], &y[2]); n != 3 || err != nil {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}
func (r *Registry) recordCandidate(ctx context.Context, serverID, appID, stage string, build RetainedBuild, node, npm string) (Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := Release{}
	_, err := r.changeLocked("lifecycle", appID, nil, &lifecycleUpdate{serverID: serverID, ctx: ctx, metadata: func(record *Record) error {
		version := build.Manifest.ReleaseVersion()
		for _, prior := range record.ReleaseHistory {
			if prior.ReleaseVersion == version {
				// Verify before installation: a retry must never recreate recorded bytes.
				checked, e := VerifyBuild(ctx, r.root, serverID, appID, prior.BuildID)
				if e != nil {
					return e
				}
				if checked.BuildID != build.BuildID || checked.Manifest.ReleaseVersion() != version {
					return ErrConflict
				}
				lock, e := candidateRead(prior.BuildRoot, "package-lock.json")
				if e != nil {
					return e
				}
				if digestBytes(lock) != prior.LockfileDigest {
					return ErrInvalidStorage
				}
				result = prior
				return nil
			}
		}
		if record.ActiveRelease != "" && !newerCandidate(version, record.ActiveRelease) {
			return ErrConflict
		}
		for _, prior := range record.ReleaseHistory {
			if !newerCandidate(version, prior.ReleaseVersion) {
				return ErrConflict
			}
		}
		installed, e := InstallBuild(ctx, r.root, stage, serverID, appID)
		if e != nil {
			return e
		}
		if installed.BuildID != build.BuildID {
			return ErrConflict
		}
		checked, e := VerifyBuild(ctx, r.root, serverID, appID, installed.BuildID)
		if e != nil {
			return e
		}
		if checked.BuildID != build.BuildID {
			return ErrConflict
		}
		root := filepath.Join(r.root, appID, "builds", checked.BuildID)
		lock, e := candidateRead(root, "package-lock.json")
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		result = Release{ReleaseVersion: version, BuildID: checked.BuildID, BuildRoot: root, NodeVersion: node, NPMVersion: npm, LockfileDigest: digestBytes(lock)}
		record.ReleaseHistory = append(record.ReleaseHistory, result)
		return nil
	}})
	if err != nil {
		return Release{}, err
	}
	return result, nil
}
