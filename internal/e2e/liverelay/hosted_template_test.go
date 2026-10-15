//go:build e2e_liverelay

package liverelay

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestLiveRelay_HostedTemplate(t *testing.T) {
	node, fixture := os.Getenv("HOSTED_APP_NODE"), os.Getenv("HOSTED_APP_TEMPLATE_BUILD")
	if node == "" || fixture == "" {
		t.Fatal("actual-template acceptance requires HOSTED_APP_NODE and HOSTED_APP_TEMPLATE_BUILD")
	}
	node, err := filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(hostedCommand(t, node, "", nil, "--version")))
	var major, minor, patch int
	if n, err := fmt.Sscanf(version, "v%d.%d.%d", &major, &minor, &patch); err != nil || n != 3 || major != 24 || minor < 15 || patch < 0 {
		t.Fatal("unsupported actual Node", version)
	}
	t.Logf("actual Node %s; compiled maintained template %s", version, fixture)
	r := startRelay(t, ensureRelayBuilt(t))
	home := hostedHome(t)
	pair := hostedPair(t, home, r)
	server := readPersistedServerID(t, home)
	build, data := hostedSeed(t, home, server)
	for _, part := range []string{"dist/service", "dist/web", "sql/migrations", "package.json"} {
		err := filepath.WalkDir(filepath.Join(fixture, part), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(fixture, path)
			if err != nil {
				return err
			}
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(build, rel), 0700)
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("nonregular template input %s", rel)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(build, rel), b, 0600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PYRY_APP_ID=" + hostedAppID, "PYRY_SERVER_ID=" + server, "PYRY_APP_RELEASE=1.0.0", "PYRY_APP_DATA_DIR=" + data, "PYRY_APP_DB_PATH=" + filepath.Join(data, "app.sqlite"), "PYRY_APP_BIND_HOST=127.0.0.1", "PYRY_APP_PORT=12345", "NODE_ENV=production"}
	hostedCommand(t, node, build, env, "dist/service/migrate.js")
	hostedCommand(t, node, "", nil, "--input-type=module", "-e", `import {DatabaseSync} from 'node:sqlite'; const db=new DatabaseSync(process.argv[1]); db.prepare('INSERT INTO main.preferences VALUES (1,1,?,?)').run('overview','24h'); db.close();`, filepath.Join(data, "app.sqlite"))
	d := spawnDaemon(t, home, r.baseWS, hostedLauncher(t, "exec "+hostedQuote(node)+" \"$@\"")...)
	hostedRunning(t, home, d)
	r.waitBinaryRegistered(t, 10*time.Second)
	p := hostedConnect(t, r, home, pair)
	read := hostedAPI()
	read.Path = "/api/preferences"
	p.request(10, protocol.TypeAppAPIRequest, read)
	p.response(10, 200, []byte(`{"version":1,"selected_tab":"overview","period":"24h"}`), false)
	want := []byte(`{"version":1,"selected_tab":"preferences","period":"7d"}`)
	write := read
	write.Method = "POST"
	write.Headers = map[string]string{"content-type": "application/json"}
	write.Body = base64.StdEncoding.EncodeToString(want)
	p.request(11, protocol.TypeAppAPIRequest, write)
	p.response(11, 204, nil, false)
	// response verifies zero chunks/bytes and empty digest, sends no credit,
	// and rejects any completion frame before the next encrypted request.
	p.request(12, protocol.TypeAppAPIRequest, read)
	p.response(12, 200, want, false)
	d.stop(t)
	// Template bytes stay unchanged; all writes belong to the persistent data root.
	got, err := os.ReadFile(filepath.Join(build, "dist/service/main.js"))
	source, sourceErr := os.ReadFile(filepath.Join(fixture, "dist/service/main.js"))
	if err != nil || sourceErr != nil || !bytes.Equal(got, source) {
		t.Fatal("template service changed")
	}
}
