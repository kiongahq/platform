package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine implements the Docker API routes the driver uses, including the exec
// hijack, so framing and the end-of-stream rules are tested without an engine.
type fakeEngine struct {
	mu        sync.Mutex
	calls     []string
	runtimes  map[string]any
	created   []map[string]any
	createSt  []int
	exitCode  int
	frames    func(conn io.Writer, stdin io.Reader)
	execBody  map[string]any
	upgrade   bool
	execState []string
}

func frame(stream byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/info":
		_ = json.NewEncoder(w).Encode(map[string]any{"Runtimes": f.runtimes})
	case r.URL.Path == "/volumes/create":
		w.WriteHeader(http.StatusCreated)
	case r.URL.Path == "/containers/create":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.created = append(f.created, body)
		status := http.StatusCreated
		if len(f.createSt) > 0 {
			status, f.createSt = f.createSt[0], f.createSt[1:]
		}
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"Id":"c1","message":"m"}`))
	case strings.HasSuffix(r.URL.Path, "/start") && strings.HasPrefix(r.URL.Path, "/containers/"):
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/images/create":
		_, _ = w.Write([]byte(`{"status":"pulling"}` + "\n"))
	case strings.HasSuffix(r.URL.Path, "/exec"):
		_ = json.NewDecoder(r.Body).Decode(&f.execBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"e1"}`))
	case r.URL.Path == "/exec/e1/start":
		_, _ = io.Copy(io.Discard, r.Body)
		hijacker := w.(http.Hijacker)
		conn, buffered, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if f.upgrade {
			_, _ = buffered.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		} else {
			_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
		}
		_ = buffered.Flush()
		if f.frames != nil {
			f.frames(conn, buffered)
		}
	case r.URL.Path == "/exec/e1/json":
		f.mu.Lock()
		running := len(f.execState) > 0
		if running {
			f.execState = f.execState[1:]
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"Running": running, "ExitCode": f.exitCode})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") && strings.HasPrefix(r.URL.Path, "/containers/missing"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such container"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/containers/podman/json":
		_, _ = w.Write([]byte(`{"Config":{"Labels":{"a":"b"}},"State":{"Running":true},"HostConfig":{"Runtime":"oci"}}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") && strings.HasPrefix(r.URL.Path, "/containers/") && r.URL.Path != "/containers/json":
		_, _ = w.Write([]byte(`{"Config":{"Labels":{"a":"b"}},"State":{"Running":true},"HostConfig":{"Runtime":"runsc"}}`))
	case r.URL.Path == "/containers/json":
		_, _ = w.Write([]byte(`[{"Names":["/one"],"Labels":{"a":"b"},"State":"running"},{"Names":[],"Labels":{}}]`))
	case r.URL.Path == "/volumes":
		_, _ = w.Write([]byte(`{"Volumes":[{"Name":"v","Labels":{"a":"b"}}]}`))
	case r.Method == http.MethodDelete:
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func engine(t *testing.T, fake *fakeEngine) *Docker {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.CloseClientConnections)
	t.Cleanup(server.Close)
	return &Docker{BaseURL: server.URL}
}

func TestDockerRuntimeRegistered(t *testing.T) {
	driver := engine(t, &fakeEngine{runtimes: map[string]any{"runsc": map[string]any{}, "runc": map[string]any{}}})
	for runtime, want := range map[string]bool{"runsc": true, "runc": true, "kata": false} {
		got, err := driver.RuntimeRegistered(context.Background(), runtime)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v", runtime, got, err)
		}
	}
	broken := &Docker{BaseURL: "http://127.0.0.1:1"}
	if _, err := broken.RuntimeRegistered(context.Background(), "runsc"); err == nil {
		t.Fatal("an unreachable engine must be an error, so a caller fails closed")
	}
}

func testSpec() ContainerSpec {
	return ContainerSpec{Name: "kionga-sandbox-x", Image: testImage, Runtime: "runsc", Labels: map[string]string{"a": "b"}, Command: []string{"/bin/sh", "-c", "x"}, Network: NetworkNone, MemoryBytes: 1, NanoCPUs: 1, Pids: 1, Volume: &VolumeMount{Name: "v", Path: WorkspacePath}}
}

func TestTmpfsAllowsExecExplicitly(t *testing.T) {
	spec := testSpec()
	spec.Tmpfs = []TmpfsMount{{Path: "/run/kionga", SizeBytes: 1024}}
	options := createBody(spec)["HostConfig"].(map[string]any)["Tmpfs"].(map[string]string)["/run/kionga"]
	if !strings.Contains(options, "exec") || strings.Contains(options, "noexec") {
		t.Fatalf("Docker defaults tmpfs to noexec, which stops askpass: %q", options)
	}
	if !strings.Contains(options, "nosuid") || !strings.Contains(options, "nodev") {
		t.Fatalf("%q", options)
	}
}

func TestCreateBodyDiskFields(t *testing.T) {
	spec := testSpec()
	spec.FileSizeBytes = 1024
	spec.Volume.ReadOnly = true
	raw, err := json.Marshal(createBody(spec))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `{"Hard":1024,"Name":"fsize","Soft":1024}`) {
		t.Fatalf("no file size limit: %s", raw)
	}
	if !strings.Contains(string(raw), `"ReadOnly":true,"Source":"v"`) {
		t.Fatalf("not read-only: %s", raw)
	}
}

func TestDockerStopToleratesGoneOrStopped(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusConflict} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/containers/x/kill" || r.URL.Query().Get("signal") != "KILL" {
				t.Errorf("unexpected %s", r.URL)
			}
			w.WriteHeader(status)
		}))
		if err := (&Docker{BaseURL: server.URL}).Stop(context.Background(), "x"); err != nil {
			t.Errorf("%d: %v", status, err)
		}
		server.Close()
	}
}

func TestDockerCreatePullsOnlyWhenMissingAndReportsConflict(t *testing.T) {
	fake := &fakeEngine{createSt: []int{http.StatusNotFound, http.StatusCreated}}
	driver := engine(t, fake)
	if err := driver.Create(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.calls, " | ")
	for _, want := range []string{"POST /volumes/create", "POST /images/create", "POST /containers/create", "POST /containers/kionga-sandbox-x/start"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
	if len(fake.created) != 2 || fake.created[0]["HostConfig"].(map[string]any)["Runtime"] != "runsc" {
		t.Fatalf("created: %+v", fake.created)
	}
	conflict := engine(t, &fakeEngine{createSt: []int{http.StatusConflict}})
	if err := conflict.Create(context.Background(), testSpec()); !errors.Is(err, ErrExists) {
		t.Fatalf("got %v", err)
	}
	local := testSpec()
	local.Image = "sha256:" + strings.Repeat("a", 64)
	missing := engine(t, &fakeEngine{createSt: []int{http.StatusNotFound}})
	if err := missing.Create(context.Background(), local); err == nil {
		t.Fatal("a local image ID cannot be pulled")
	}
	refused := engine(t, &fakeEngine{createSt: []int{http.StatusInternalServerError}})
	if err := refused.Create(context.Background(), testSpec()); err == nil {
		t.Fatal("an engine refusal must surface")
	}
}

func TestDockerExecFramingStdinAndExitCode(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		fake := &fakeEngine{exitCode: 3, upgrade: upgrade, execState: []string{"running"}}
		received := make(chan string, 1)
		fake.frames = func(conn io.Writer, stdin io.Reader) {
			raw := make([]byte, 5)
			_, _ = io.ReadFull(stdin, raw)
			received <- string(raw)
			_, _ = conn.Write(frame(1, "out-"))
			_, _ = conn.Write(frame(2, "err"))
			_, _ = conn.Write(frame(1, "more"))
		}
		driver := engine(t, fake)
		var stdout, stderr bytes.Buffer
		code, err := driver.Exec(context.Background(), "x", ExecRequest{Argv: []string{"a", "b"}, Stdin: strings.NewReader("input"), Stdout: &stdout, Stderr: &stderr})
		if err != nil || code != 3 {
			t.Fatalf("code %d, err %v", code, err)
		}
		if stdout.String() != "out-more" || stderr.String() != "err" {
			t.Fatalf("%q %q", stdout.String(), stderr.String())
		}
		if got := <-received; got != "input" {
			t.Fatalf("stdin %q", got)
		}
		if fake.execBody["User"] != "10001:10001" || fake.execBody["Privileged"] != false {
			t.Fatalf("exec body: %+v", fake.execBody)
		}
	}
}

// A process that left the exec's tree keeps stdout open forever. The driver must end
// on the deadline anyway, and the cancel must not wait on the stream.
func TestDockerExecDeadlineBeatsAStreamThatNeverEnds(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fake := &fakeEngine{upgrade: true, frames: func(conn io.Writer, _ io.Reader) {
		_, _ = conn.Write(frame(1, "partial"))
		<-release
	}}
	driver := engine(t, fake)
	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := driver.Exec(ctx, "x", ExecRequest{Argv: []string{"a"}, Stdout: &stdout, Stderr: io.Discard})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("took %s", time.Since(started))
	}
	if stdout.String() != "partial" {
		t.Fatalf("%q", stdout.String())
	}
}

func TestDockerExecStopsWhenWriterFails(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fake := &fakeEngine{frames: func(conn io.Writer, _ io.Reader) {
		_, _ = conn.Write(frame(1, "toolong"))
		<-release
	}}
	driver := engine(t, fake)
	_, err := driver.Exec(context.Background(), "x", ExecRequest{Argv: []string{"a"}, Stdout: &sizeLimitWriter{w: io.Discard, remaining: 2}, Stderr: io.Discard})
	if !errors.Is(err, errStreamFull) {
		t.Fatalf("got %v", err)
	}
}

func TestDockerEnsureNetworkRefusesAForeignNetwork(t *testing.T) {
	labels := map[string]string{LabelManaged: "true", LabelPrefix: "kionga-sandbox"}
	cases := map[string]struct {
		get     int
		body    string
		wantErr bool
		creates bool
	}{
		"missing":         {get: http.StatusNotFound, creates: true},
		"ours":            {get: http.StatusOK, body: `{"Labels":{"` + LabelManaged + `":"true","` + LabelPrefix + `":"kionga-sandbox"}}`},
		"compose network": {get: http.StatusOK, body: `{"Labels":{"com.docker.compose.project":"kionga"}}`, wantErr: true},
		"another install": {get: http.StatusOK, body: `{"Labels":{"` + LabelManaged + `":"true","` + LabelPrefix + `":"other"}}`, wantErr: true},
		"engine error":    {get: http.StatusInternalServerError, wantErr: true},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			var created map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_ = json.NewDecoder(r.Body).Decode(&created)
					w.WriteHeader(http.StatusCreated)
					return
				}
				w.WriteHeader(item.get)
				_, _ = w.Write([]byte(item.body))
			}))
			defer server.Close()
			driver := &Docker{BaseURL: server.URL}
			err := driver.EnsureNetwork(context.Background(), "kionga-sandbox-git", labels)
			if (err != nil) != item.wantErr {
				t.Fatalf("err %v", err)
			}
			if (created != nil) != item.creates {
				t.Fatalf("created %v", created)
			}
			if item.creates && (created["Internal"] != false || created["Options"].(map[string]any)["com.docker.network.bridge.enable_icc"] != "false") {
				t.Fatalf("created %v", created)
			}
		})
	}
}

func TestCreateBodyPinsOnlyManagerHosts(t *testing.T) {
	spec := testSpec()
	if hosts := createBody(spec)["HostConfig"].(map[string]any)["ExtraHosts"].([]string); len(hosts) != 0 {
		t.Fatalf("no hosts unless the manager adds them: %v", hosts)
	}
	spec.ExtraHosts = []HostEntry{{Host: "github.com", Address: "140.82.112.3"}, {Host: "github.com", Address: "2606:50c0:8000::153"}}
	hosts := createBody(spec)["HostConfig"].(map[string]any)["ExtraHosts"].([]string)
	if strings.Join(hosts, ",") != "github.com:140.82.112.3,github.com:2606:50c0:8000::153" {
		t.Fatalf("%v", hosts)
	}
}

func TestDemuxRejectsUnknownStream(t *testing.T) {
	if err := demux(bytes.NewReader(frame(9, "x")), io.Discard, io.Discard); err == nil {
		t.Fatal("expected an error")
	}
	// A hostile process prints bytes shaped like a frame header. They stay payload.
	forged := string(frame(2, "fake-stderr"))
	var stdout, stderr bytes.Buffer
	if err := demux(bytes.NewReader(frame(1, forged)), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != forged || stderr.Len() != 0 {
		t.Fatalf("a forged header crossed streams: %q %q", stdout.String(), stderr.String())
	}
}

func TestDockerInspectListRemove(t *testing.T) {
	driver := engine(t, &fakeEngine{})
	found, err := driver.Inspect(context.Background(), "ok")
	if err != nil || !found.Running || found.Runtime != "runsc" || found.Labels["a"] != "b" {
		t.Fatalf("%+v %v", found, err)
	}
	if podman, err := driver.Inspect(context.Background(), "podman"); err != nil || podman.Runtime != "" {
		t.Fatalf("podman's oci placeholder must read as unknown: %+v %v", podman, err)
	}
	if _, err := driver.Inspect(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	instances, err := driver.List(context.Background(), map[string]string{"a": "b"})
	if err != nil || len(instances) != 1 || instances[0].Name != "one" || !instances[0].Running {
		t.Fatalf("%+v %v", instances, err)
	}
	volumes, err := driver.ListVolumes(context.Background(), map[string]string{"a": "b"})
	if err != nil || len(volumes) != 1 {
		t.Fatalf("%+v %v", volumes, err)
	}
	if err := driver.Remove(context.Background(), "missing"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeEngine{}
	killing := engine(t, fake)
	if err := killing.Remove(context.Background(), "helper"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.calls, " | ") != "POST /containers/helper/kill | DELETE /containers/helper" {
		t.Fatalf("remove must kill first, so a helper does not outlive its job: %v", fake.calls)
	}
	if err := driver.RemoveVolume(context.Background(), "missing"); err != nil {
		t.Fatal(err)
	}
	if err := driver.Restart(context.Background(), "ok"); err != nil {
		t.Fatal(err)
	}
}
