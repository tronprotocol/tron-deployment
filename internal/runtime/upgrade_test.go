package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSwapComposeImageTag(t *testing.T) {
	tests := []struct {
		name, compose, version, want, old, image string
		wantErr                                  bool
	}{
		{"official", "image: tronprotocol/java-tron:4.8.0\n", "4.8.1", "image: tronprotocol/java-tron:4.8.1\n", "tronprotocol/java-tron:4.8.0", "tronprotocol/java-tron:4.8.1", false},
		{"registry port", "image: registry:5000/img:1.0\n", "2.0", "image: registry:5000/img:2.0\n", "registry:5000/img:1.0", "registry:5000/img:2.0", false},
		{"digest pin", "image: img:1.0@sha256:abc\n", "2.0", "image: img:2.0\n", "img:1.0@sha256:abc", "img:2.0", false},
		{"same version", "image: img:1.0\n", "1.0", "image: img:1.0\n", "img:1.0", "img:1.0", false},
		{"missing", "services:\n  node:\n    command: tron\n", "1.0", "", "", "", true},
		{"different images", "image: a:1\nother:\n  image: b:1\n", "2", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, old, image, err := swapComposeImageTag([]byte(tc.compose), tc.version)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if string(got) != tc.want || old != tc.old || image != tc.image {
				t.Fatalf("got %q, %q, %q", got, old, image)
			}
		})
	}
}

func TestJarPrepareArtifactNetworkUpgradeRequiresBackup(t *testing.T) {
	old := os.Getenv("TROND_NETWORK_UPGRADE")
	t.Setenv("TROND_NETWORK_UPGRADE", "1")
	t.Cleanup(func() { _ = os.Setenv("TROND_NETWORK_UPGRADE", old) })
	r := NewJarRuntime(&missingBackupUpgradeTarget{fakeTarget: newFakeTarget()})
	r.SetPurgeInstallPath("/install")
	_, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"})
	if err == nil || !strings.Contains(err.Error(), "no upgrade backup found") {
		t.Fatalf("error = %v, want missing network upgrade backup", err)
	}
}

func TestJarPrepareArtifactOrdinaryUpgradeDoesNotRestoreStaleBackup(t *testing.T) {
	f := &jarUpgradeFakeTarget{newFakeTarget()}
	f.files["/opt/tron/FullNode.jar.upgrade.backup"] = []byte("stale")
	r := NewJarRuntime(f)
	r.SetPurgeInstallPath("/opt/tron")
	t.Setenv("TROND_NETWORK_UPGRADE", "")
	_, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"})
	if err == nil || !strings.Contains(err.Error(), "jar URL is required") {
		t.Fatalf("error = %v, want ordinary upgrade URL validation", err)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("ordinary upgrade attempted restore commands: %v", f.cmds)
	}
}

type upgradeFakeTarget struct {
	*fakeTarget
	pullErr error
}

type jarUpgradeFakeTarget struct{ *fakeTarget }

type backupJarUpgradeFakeTarget struct{ *jarUpgradeFakeTarget }

type missingBackupUpgradeTarget struct{ *fakeTarget }

func (f *missingBackupUpgradeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	if cmd == "test" {
		return nil, errors.New("missing")
	}
	return f.fakeTarget.Exec(ctx, cmd, args...)
}

type remoteJarUpgradeFakeTarget struct {
	*jarUpgradeFakeTarget
	uploads    int
	remotePath string
	putErr     error
}

func (f *remoteJarUpgradeFakeTarget) IsRemote() bool { return true }
func (f *remoteJarUpgradeFakeTarget) PutFile(ctx context.Context, localPath, remotePath string) error {
	f.uploads++
	f.remotePath = remotePath
	return f.putErr
}

type failingUpgradeTarget struct {
	*fakeTarget
	writeErr      error
	startErr      error
	startFailures int
	mvFailAt      int
	mvCount       int
}

func (f *failingUpgradeTarget) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	return f.fakeTarget.WriteFile(ctx, path, data, perm)
}

func (f *failingUpgradeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	if cmd == "docker" && len(args) > 0 && args[len(args)-1] == "--force-recreate" {
		if f.startFailures > 0 {
			f.startFailures--
			return nil, f.startErr
		}
	}
	if cmd == "mv" {
		f.mvCount++
		if f.mvFailAt == f.mvCount {
			return nil, fmt.Errorf("mv %s to %s failed", args[0], args[1])
		}
	}
	return nil, nil
}

func (f *jarUpgradeFakeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	if cmd == "test" {
		return nil, errors.New("missing")
	}
	if cmd == "sha256sum" {
		return []byte("deadbeef  /opt/tron/FullNode.jar\n"), nil
	}
	return nil, nil
}

func (f *backupJarUpgradeFakeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	if cmd == "test" {
		return nil, nil
	}
	return nil, nil
}

func (f *upgradeFakeTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{cmd}, args...))
	if cmd == "docker" && len(args) >= 2 && args[0] == "pull" {
		return nil, f.pullErr
	}
	return nil, nil
}

func TestDockerRuntimeUpgradeArtifact(t *testing.T) {
	path := "/deploy/n1/docker-compose.yaml"
	f := &upgradeFakeTarget{fakeTarget: newFakeTarget()}
	f.files[path] = []byte("services:\n  n1:\n    image: img:1\n")
	tx, err := NewDockerRuntime(f, "/deploy").PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := string(f.files[path]); !strings.Contains(got, "img:2") {
		t.Fatalf("compose not updated: %s", got)
	}
	if !reflect.DeepEqual(f.cmds[0], []string{"docker", "pull", "img:2"}) {
		t.Fatalf("commands = %v", f.cmds)
	}

	f = &upgradeFakeTarget{fakeTarget: newFakeTarget(), pullErr: errors.New("no image")}
	f.files[path] = []byte("image: img:1\n")
	if _, err := NewDockerRuntime(f, "/deploy").PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"}); err == nil {
		t.Fatal("expected pull error")
	}
	if string(f.files[path]) != "image: img:1\n" {
		t.Fatal("compose changed after failed pull")
	}
}

func TestDockerArtifactActivateWriteFailureDoesNotStart(t *testing.T) {
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget(), writeErr: errors.New("write failed")}
	tx := &dockerArtifactTransaction{r: NewDockerRuntime(f, "/deploy"), name: "n1", path: "/deploy/n1/docker-compose.yaml", updated: []byte("new")}
	if err := tx.Activate(context.Background()); err == nil {
		t.Fatal("Activate succeeded despite compose write failure")
	}
	for _, cmd := range f.cmds {
		if len(cmd) > 0 && cmd[0] == "docker" {
			t.Fatal("docker Start command was invoked after Activate failure")
		}
	}
}

func TestDockerArtifactStartFailureRollbackRestoresCompose(t *testing.T) {
	old := []byte("image: img:1\n")
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget(), startErr: errors.New("up failed"), startFailures: 1}
	path := "/deploy/n1/docker-compose.yaml"
	tx := &dockerArtifactTransaction{r: NewDockerRuntime(f, "/deploy"), name: "n1", path: path, old: old, updated: []byte("image: img:2\n")}
	if err := tx.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded unexpectedly")
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.files[path], old) {
		t.Fatalf("compose = %q, want old compose %q", f.files[path], old)
	}
	if got := countCommand(f.cmds, "docker"); got != 2 {
		t.Fatalf("docker compose calls = %d, want failed up plus rollback up", got)
	}
}

func TestJarArtifactActivateSecondMoveSelfRestoresBackup(t *testing.T) {
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget(), mvFailAt: 2}
	tx := &jarArtifactTransaction{r: NewJarRuntime(f), candidate: "/opt/tron/FullNode.jar.candidate", live: "/opt/tron/FullNode.jar", backup: "/opt/tron/FullNode.jar.upgrade.backup"}
	if err := tx.Activate(context.Background()); err == nil {
		t.Fatal("Activate succeeded despite candidate move failure")
	} else if !strings.Contains(err.Error(), "/opt/tron/FullNode.jar.candidate") {
		t.Fatalf("error lacks artifact paths: %v", err)
	}
	if got := f.cmds[len(f.cmds)-1]; !reflect.DeepEqual(got, []string{"mv", "/opt/tron/FullNode.jar.upgrade.backup", "/opt/tron/FullNode.jar"}) {
		t.Fatalf("last recovery command = %v", got)
	}
}

func TestJarArtifactRollbackSequence(t *testing.T) {
	f := &failingUpgradeTarget{fakeTarget: newFakeTarget()}
	tx := &jarArtifactTransaction{r: NewJarRuntime(f), name: "n1", candidate: "/opt/tron/FullNode.jar.candidate", live: "/opt/tron/FullNode.jar", backup: "/opt/tron/FullNode.jar.upgrade.backup", activated: true}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"mv", tx.live, tx.candidate}, {"mv", tx.backup, tx.live}, {"systemctl", "start", "tron-n1.service"}}
	if !reflect.DeepEqual(f.cmds, want) {
		t.Fatalf("rollback commands = %v, want %v", f.cmds, want)
	}
}

func TestValidateUpgradeVersionRejectsUnsafeTags(t *testing.T) {
	for _, version := range []string{"../../etc", "tag with spaces"} {
		if err := validateUpgradeVersion(version); err == nil {
			t.Errorf("validateUpgradeVersion(%q) accepted unsafe tag", version)
		}
	}
}

func countCommand(cmds [][]string, name string) int {
	count := 0
	for _, cmd := range cmds {
		if len(cmd) > 0 && cmd[0] == name {
			count++
		}
	}
	return count
}

func TestJarRuntimeUpgradeArtifact(t *testing.T) {
	t.Run("missing URL", func(t *testing.T) {
		r := NewJarRuntime(&jarUpgradeFakeTarget{newFakeTarget()})
		r.SetPurgeInstallPath("/opt/tron")
		if _, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2"}); err == nil || !strings.Contains(err.Error(), "jar URL") {
			t.Fatal(err)
		}
	})
	t.Run("ssh downloads locally and uploads", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("jar-bytes"))
		}))
		defer server.Close()
		f := &remoteJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}}
		r := NewJarRuntime(f)
		if err := r.downloadJar(context.Background(), server.URL, "/opt/tron/FullNode.jar", ""); err != nil {
			t.Fatal(err)
		}
		if f.uploads != 1 {
			t.Fatalf("uploads = %d, want 1", f.uploads)
		}
		if f.remotePath != "/opt/tron/FullNode.jar.upgrade.tmp" {
			t.Fatalf("upload path = %q", f.remotePath)
		}
		if want := []string{"mv", "/opt/tron/FullNode.jar.upgrade.tmp", "/opt/tron/FullNode.jar"}; !reflect.DeepEqual(f.cmds[len(f.cmds)-1], want) {
			t.Fatalf("final command = %v, want %v", f.cmds[len(f.cmds)-1], want)
		}
		for _, cmd := range f.cmds {
			if len(cmd) > 0 && cmd[0] == "curl" {
				t.Fatalf("remote curl used: %v", f.cmds)
			}
		}
	})
	t.Run("SHA mismatch does not upload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("jar-bytes")) }))
		defer server.Close()
		f := &remoteJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}}
		err := NewJarRuntime(f).downloadJar(context.Background(), server.URL, "/opt/tron/FullNode.jar", strings.Repeat("0", 64))
		if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
			t.Fatalf("error = %v", err)
		}
		if f.uploads != 0 {
			t.Fatalf("uploads = %d, want 0", f.uploads)
		}
	})
	t.Run("oversized response does not upload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint((512<<20)+1))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		f := &remoteJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}}
		err := NewJarRuntime(f).downloadJar(context.Background(), server.URL, "/opt/tron/FullNode.jar", "")
		if err == nil || !strings.Contains(err.Error(), "maximum JAR size") {
			t.Fatalf("error = %v", err)
		}
		if f.uploads != 0 {
			t.Fatalf("uploads = %d, want 0", f.uploads)
		}
	})
	t.Run("non-2xx response does not upload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusBadGateway) }))
		defer server.Close()
		f := &remoteJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}}
		err := NewJarRuntime(f).downloadJar(context.Background(), server.URL, "/opt/tron/FullNode.jar", "")
		if err == nil || !strings.Contains(err.Error(), "http 502") {
			t.Fatalf("error = %v", err)
		}
		if f.uploads != 0 {
			t.Fatalf("uploads = %d, want 0", f.uploads)
		}
	})
	t.Run("upload failure cleans temporary remote file", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("jar-bytes")) }))
		defer server.Close()
		f := &remoteJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}, putErr: errors.New("upload failed")}
		err := NewJarRuntime(f).downloadJar(context.Background(), server.URL, "/opt/tron/FullNode.jar", "")
		if err == nil {
			t.Fatal("expected upload failure")
		}
		if len(f.cmds) != 1 || !reflect.DeepEqual(f.cmds[0], []string{"rm", "-f", "/opt/tron/FullNode.jar.upgrade.tmp"}) {
			t.Fatalf("cleanup commands = %v", f.cmds)
		}
	})
	t.Run("download and restart", func(t *testing.T) {
		f := &jarUpgradeFakeTarget{newFakeTarget()}
		r := NewJarRuntime(f)
		r.SetPurgeInstallPath("/opt/tron")
		tx, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2", JarURL: "https://example/jar"})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Activate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.cmds[0][0] != "curl" || f.cmds[len(f.cmds)-1][0] != "mv" {
			t.Fatalf("commands = %v", f.cmds)
		}
	})
	t.Run("verified existing jar", func(t *testing.T) {
		f := &jarUpgradeFakeTarget{newFakeTarget()}
		f.files["/opt/tron/FullNode.jar"] = []byte("old")
		r := NewJarRuntime(f)
		r.SetPurgeInstallPath("/opt/tron")
		tx, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "2", JarURL: "https://example/jar", JarSHA256: "deadbeef"})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Activate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.cmds[0][0] != "curl" {
			t.Fatalf("commands = %v", f.cmds)
		}
	})
}

func TestJarArtifactCleanupPreservesBackupForNetworkUpgrade(t *testing.T) {
	f := &backupJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}}
	r := NewJarRuntime(f)
	t.Setenv("TROND_PRESERVE_BACKUP", "1")
	tx := &jarArtifactTransaction{r: r, candidate: "/opt/tron/FullNode.jar.candidate", live: "/opt/tron/FullNode.jar", backup: "/opt/tron/FullNode.jar.upgrade.backup"}
	if err := tx.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.cmds[len(f.cmds)-1]; !reflect.DeepEqual(got, []string{"rm", "-f", tx.candidate}) {
		t.Fatalf("cleanup commands = %v, want candidate-only removal", f.cmds)
	}
}

func TestJarArtifactCleanupDeletesBackupWithoutPreserveSignal(t *testing.T) {
	f := &backupJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}}
	t.Setenv("TROND_NETWORK_UPGRADE", "")
	t.Setenv("TROND_PRESERVE_BACKUP", "")
	tx := &jarArtifactTransaction{r: NewJarRuntime(f), candidate: "/opt/tron/FullNode.jar.candidate", live: "/opt/tron/FullNode.jar", backup: "/opt/tron/FullNode.jar.upgrade.backup"}
	if err := tx.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.cmds[len(f.cmds)-1]; !reflect.DeepEqual(got, []string{"rm", "-f", tx.candidate, tx.backup}) {
		t.Fatalf("cleanup commands = %v, want candidate and backup removal", f.cmds)
	}
}

func TestJarArtifactRollbackConsumesPreservedBackupWithoutURL(t *testing.T) {
	f := &backupJarUpgradeFakeTarget{jarUpgradeFakeTarget: &jarUpgradeFakeTarget{newFakeTarget()}}
	r := NewJarRuntime(f)
	r.SetPurgeInstallPath("/opt/tron")
	t.Setenv("TROND_NETWORK_UPGRADE", "1")
	tx, err := r.PrepareArtifact(context.Background(), "n1", UpgradeOpts{Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.cmds; !reflect.DeepEqual(got, [][]string{
		{"test", "-e", "/opt/tron/FullNode.jar.upgrade.backup"},
		{"mv", "/opt/tron/FullNode.jar", "/opt/tron/FullNode.jar.candidate"},
		{"mv", "/opt/tron/FullNode.jar.upgrade.backup", "/opt/tron/FullNode.jar"},
		{"systemctl", "start", "tron-n1.service"},
	}) {
		t.Fatalf("commands = %v", got)
	}
}

func TestJarDeployInterruptedArtifactConvergesOnRerun(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded string
		restart  bool
	}{
		{name: "missing recorded digest", recorded: "", restart: true},
		{name: "matching recorded digest", recorded: "deadbeef", restart: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &jarUpgradeFakeTarget{newFakeTarget()}
			f.files["/opt/tron/FullNode.jar"] = []byte("new artifact")
			r := NewJarRuntime(f)
			err := r.Deploy(context.Background(), DeployOpts{
				Name:           "n1",
				JarPath:        "/opt/tron/FullNode.jar",
				JarURL:         "https://example/jar",
				JarSHA256:      "deadbeef",
				ArtifactSHA256: tc.recorded,
				ConfigData:     []byte("config"),
				SystemdData:    []byte("unit"),
			})
			if err != nil {
				t.Fatal(err)
			}
			gotRestart := false
			for _, cmd := range f.cmds {
				if len(cmd) >= 2 && cmd[0] == "systemctl" && cmd[1] == "restart" {
					gotRestart = true
				}
			}
			if gotRestart != tc.restart {
				t.Fatalf("restart = %v, want %v; commands = %v", gotRestart, tc.restart, f.cmds)
			}
		})
	}
}
