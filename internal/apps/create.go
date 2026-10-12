package apps

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	hostedtemplate "github.com/pyrycode/pyrycode/templates/hosted-app"
)

var (
	ErrMintIdentity        = errors.New("apps: cannot mint app identity; retry creation")
	ErrIdentityUnavailable = errors.New("apps: app identity or destination occupied; retry with a fresh identity")
	ErrSourceWrite         = errors.New("apps: source generation failed; check private app storage and retain the unregistered attempt")
	ErrCreatePersistence   = errors.New("apps: registration persistence failed; check private registry storage; generated source remains unregistered")
)

// Source describes editable locations and committed registration state, not readiness.
// Revision is the host revision; Record.Revision is the app revision.
type Source struct {
	ServerID, AppID          string
	SourcePath, ManifestPath string
	Record                   Record
	Revision                 uint64
}

type sourceFile struct {
	name string
	data []byte
}

// Create mints an identity and generates bundled editable source before registration.
// It executes no commands. Failed attempts may retain unregistered source.
func (r *Registry) Create(ctx context.Context, title string) (Source, error) {
	return r.create(ctx, title, mintAppID, writeSource)
}

func mintAppID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrMintIdentity
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (r *Registry) create(ctx context.Context, title string, mint func() (string, error), generate func(context.Context, string, string, []sourceFile) error) (Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	// Check the raw title before JSON encoding can replace invalid UTF-8.
	if !validText(title, 128, true) {
		return Source{}, ErrInvalidManifest
	}
	if len(r.snapshot.Records) >= maxApps {
		return Source{}, ErrCapacity
	}
	if r.snapshot.Revision == maxRevision {
		return Source{}, ErrExhausted
	}
	id, err := mint()
	if err != nil {
		return Source{}, ErrMintIdentity
	}
	if !validID(id) {
		return Source{}, ErrInvalidIdentity
	}
	if _, err := r.sourceLocked(id); !errors.Is(err, ErrNotFound) {
		return Source{}, ErrIdentityUnavailable
	}
	manifest, err := templateManifest(r.snapshot.ServerID, id, title)
	if err != nil {
		return Source{}, err
	}
	files, err := templateFiles(manifest)
	if err != nil {
		return Source{}, ErrSourceWrite
	}
	if err = generate(ctx, r.root, id, files); err != nil {
		if ctx.Err() != nil {
			return Source{}, ctx.Err()
		}
		if errors.Is(err, ErrIdentityUnavailable) {
			return Source{}, ErrIdentityUnavailable
		}
		return Source{}, ErrSourceWrite
	}
	data, err := readSourceManifest(r.root, id)
	if err != nil {
		return Source{}, ErrSourceWrite
	}
	written, err := ValidateManifest(data, r.snapshot.ServerID)
	if err != nil {
		return Source{}, err
	}
	if written != manifest {
		return Source{}, ErrInvalidManifest
	}
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	if _, err := r.changeLocked("register", "", data, nil); err != nil {
		return Source{}, ErrCreatePersistence
	}
	return r.sourceLocked(id)
}

func templateManifest(serverID, appID, title string) (Manifest, error) {
	data, err := hostedtemplate.Assets.ReadFile("manifest.json")
	if err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	fields["server_id"], fields["app_id"], fields["title"], fields["release_version"] = serverID, appID, title, "1.0.0"
	data, err = json.Marshal(fields)
	if err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	return ValidateManifest(data, serverID)
}

func templateFiles(manifest Manifest) ([]sourceFile, error) {
	data, err := hostedtemplate.Assets.ReadFile("inventory.json")
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, err
	}
	files := make([]sourceFile, 0, len(names)+2)
	for _, name := range names {
		data, err := hostedtemplate.Assets.ReadFile("source/" + name)
		if err != nil {
			return nil, err
		}
		files = append(files, sourceFile{name, data})
	}
	title, err := json.Marshal(map[string]string{"title": manifest.Title()})
	if err != nil {
		return nil, err
	}
	return append(files, sourceFile{"app.json", manifest.JSON()}, sourceFile{"src/web/title.json", title}), nil
}

// Inspect resolves only a committed local identity. It does not read or restamp source.
func (r *Registry) Inspect(ctx context.Context, serverID, appID string) (Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	if !validID(serverID) || !validID(appID) {
		return Source{}, ErrInvalidIdentity
	}
	if serverID != r.snapshot.ServerID {
		return Source{}, ErrForeignHost
	}
	return r.sourceLocked(appID)
}

func (r *Registry) sourceLocked(appID string) (Source, error) {
	for _, record := range r.snapshot.Records {
		if record.AppID == appID {
			source := filepath.Join(r.root, appID, "source")
			return Source{r.snapshot.ServerID, appID, source, filepath.Join(source, "app.json"), cloneRecord(record), r.snapshot.Revision}, nil
		}
	}
	for _, dead := range r.snapshot.Tombstones {
		if dead.AppID == appID {
			return Source{}, ErrRemoved
		}
	}
	return Source{}, ErrNotFound
}
