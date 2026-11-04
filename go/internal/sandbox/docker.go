package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Docker talks to the Docker Engine API over a unix socket, or a Podman socket through
// its Docker-compatible API, following serving/internal/serving/manager.go.
type Docker struct {
	SocketPath string
	// BaseURL replaces the socket in tests.
	BaseURL string
	// APIVersion pins the API path, such as v1.44. Empty uses the daemon's newest.
	APIVersion string

	client *http.Client
}

// NewDocker returns a driver for the socket.
func NewDocker(socketPath, apiVersion string) *Docker {
	return &Docker{SocketPath: socketPath, APIVersion: strings.TrimSpace(apiVersion)}
}

var _ Driver = (*Docker)(nil)

// Name identifies the driver in /healthz.
func (d *Docker) Name() string { return "docker" }

func (d *Docker) dial(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	if d.BaseURL != "" {
		parsed, err := url.Parse(d.BaseURL)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, "tcp", parsed.Host)
	}
	return dialer.DialContext(ctx, "unix", d.SocketPath)
}

func (d *Docker) http() *http.Client {
	if d.client == nil {
		d.client = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return d.dial(ctx) },
		}}
	}
	return d.client
}

func (d *Docker) url(path string) string {
	base := "http://docker"
	if d.APIVersion != "" {
		base += "/" + d.APIVersion
	}
	return base + path
}

type engineError struct {
	status  int
	message string
}

func (e *engineError) Error() string {
	return fmt.Sprintf("container engine returned %d: %s", e.status, e.message)
}

func (d *Docker) do(ctx context.Context, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, d.url(path), body)
	if err != nil {
		return 0, err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := d.http().Do(request) // #nosec G704 -- the operator-configured engine socket
	if err != nil {
		return 0, fmt.Errorf("container engine unreachable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if response.StatusCode >= 400 {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &failure)
		return response.StatusCode, &engineError{status: response.StatusCode, message: failure.Message}
	}
	if output != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, output); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}

// RuntimeRegistered reads the engine's runtime list.
func (d *Docker) RuntimeRegistered(ctx context.Context, runtime string) (bool, error) {
	var info struct {
		Runtimes map[string]json.RawMessage `json:"Runtimes"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/info", nil, &info); err != nil {
		return false, err
	}
	_, ok := info.Runtimes[runtime]
	return ok, nil
}

// createBody is the only place a container's isolation is written down. Every field
// is either fixed or comes from the limits in ContainerSpec. There is no Env, Binds,
// Devices, CapAdd or Privileged input, and no engine socket is ever mounted.
func createBody(spec ContainerSpec) map[string]any {
	tmpfs := map[string]string{}
	for _, mount := range spec.Tmpfs {
		// No uid= option: Podman's compatibility API refuses it. Mode 1777 lets the fixed
		// non-root user write, and scripts create anything private with umask 077. exec is
		// explicit because Docker's default is noexec, and Podman's is not: the askpass
		// program runs from here, and agents build and run programs in /tmp.
		tmpfs[mount.Path] = fmt.Sprintf("rw,exec,nosuid,nodev,size=%d,mode=1777", mount.SizeBytes)
	}
	mounts := []map[string]any{}
	if spec.Volume != nil {
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": spec.Volume.Name, "Target": spec.Volume.Path, "ReadOnly": spec.Volume.ReadOnly})
	}
	extraHosts := []string{}
	for _, entry := range spec.ExtraHosts {
		extraHosts = append(extraHosts, entry.Host+":"+entry.Address)
	}
	ulimits := []map[string]any{{"Name": "core", "Soft": 0, "Hard": 0}}
	if spec.FileSizeBytes > 0 {
		ulimits = append(ulimits, map[string]any{"Name": "fsize", "Soft": spec.FileSizeBytes, "Hard": spec.FileSizeBytes})
	}
	host := map[string]any{
		"ExtraHosts":      extraHosts,
		"Runtime":         spec.Runtime,
		"NetworkMode":     spec.Network,
		"ReadonlyRootfs":  true,
		"CapDrop":         []string{"ALL"},
		"SecurityOpt":     []string{"no-new-privileges"},
		"Privileged":      false,
		"PidsLimit":       spec.Pids,
		"Memory":          spec.MemoryBytes,
		"MemorySwap":      spec.MemoryBytes,
		"NanoCpus":        spec.NanoCPUs,
		"Tmpfs":           tmpfs,
		"Mounts":          mounts,
		"Binds":           []string{},
		"RestartPolicy":   map[string]any{"Name": "no"},
		"PublishAllPorts": false,
		"IpcMode":         "private",
		"Ulimits":         ulimits,
	}
	return map[string]any{
		"Image":           spec.Image,
		"Entrypoint":      spec.Command[:1],
		"Cmd":             spec.Command[1:],
		"User":            fmt.Sprintf("%d:%d", UID, GID),
		"WorkingDir":      "/",
		"Env":             []string{safePath},
		"Hostname":        "sandbox",
		"Labels":          spec.Labels,
		"NetworkDisabled": spec.Network == NetworkNone,
		"AttachStdin":     false,
		"OpenStdin":       false,
		"Tty":             false,
		"HostConfig":      host,
	}
}

// EnsureNetwork creates the helpers' network, with traffic between its containers
// disabled, or accepts one this manager created earlier. A network of that name without
// our labels might be a Compose network, so it is refused rather than joined.
func (d *Docker) EnsureNetwork(ctx context.Context, name string, labels map[string]string) error {
	var existing struct {
		Labels map[string]string `json:"Labels"`
	}
	status, err := d.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, &existing)
	if err == nil {
		if existing.Labels[LabelManaged] != "true" || existing.Labels[LabelPrefix] != labels[LabelPrefix] {
			return fmt.Errorf("network %s exists and was not created by this manager", name)
		}
		return nil
	}
	if status != http.StatusNotFound {
		return err
	}
	body := map[string]any{
		"Name": name, "Driver": "bridge", "Internal": false, "Attachable": false, "Labels": labels,
		"Options": map[string]string{"com.docker.network.bridge.enable_icc": "false"},
	}
	status, err = d.do(ctx, http.MethodPost, "/networks/create", body, nil)
	if status == http.StatusConflict {
		return nil // a concurrent helper just made it
	}
	return err
}

// Create makes the volume, then the container, and starts it. The image is pulled
// only if the engine lacks it, and only callers holding an allowlisted image get here.
func (d *Docker) Create(ctx context.Context, spec ContainerSpec) error {
	if spec.Volume != nil {
		volume := map[string]any{"Name": spec.Volume.Name, "Labels": spec.Labels}
		if _, err := d.do(ctx, http.MethodPost, "/volumes/create", volume, nil); err != nil {
			return err
		}
	}
	path := "/containers/create?name=" + url.QueryEscape(spec.Name)
	body := createBody(spec)
	status, err := d.do(ctx, http.MethodPost, path, body, nil)
	if status == http.StatusNotFound {
		if err := d.pull(ctx, spec.Image); err != nil {
			return err
		}
		status, err = d.do(ctx, http.MethodPost, path, body, nil)
	}
	if status == http.StatusConflict {
		return ErrExists
	}
	if err != nil {
		return err
	}
	_, err = d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(spec.Name)+"/start", nil, nil)
	return err
}

func (d *Docker) pull(ctx context.Context, image string) error {
	if !strings.Contains(image, "@sha256:") {
		return errors.New("a local image ID cannot be pulled: " + image)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url("/images/create?fromImage="+url.QueryEscape(image)), nil)
	if err != nil {
		return err
	}
	response, err := d.http().Do(request) // #nosec G704 -- the operator-configured engine socket
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 400 {
		return fmt.Errorf("pull returned %d", response.StatusCode)
	}
	// The engine reports failure inside the 200 stream.
	decoder := json.NewDecoder(response.Body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&line); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if line.Error != "" {
			return errors.New("pull failed: " + line.Error)
		}
	}
}

type dockerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	HostConfig struct {
		Runtime string `json:"Runtime"`
	} `json:"HostConfig"`
}

// Inspect reports the instance. Podman's compatibility API answers "oci" for every
// runtime, which says nothing, so that is treated as unknown.
func (d *Docker) Inspect(ctx context.Context, name string) (Instance, error) {
	var found dockerInspect
	status, err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &found)
	if status == http.StatusNotFound {
		return Instance{}, ErrNotFound
	}
	if err != nil {
		return Instance{}, err
	}
	runtime := found.HostConfig.Runtime
	if runtime == "oci" {
		runtime = ""
	}
	identity := ""
	if found.ID != "" && found.State.StartedAt != "" {
		identity = found.ID + "/" + found.State.StartedAt
	}
	return Instance{Identity: identity, Name: name, Labels: found.Config.Labels, Running: found.State.Running, Runtime: runtime}, nil
}

func labelFilter(labels map[string]string) string {
	filter := []string{}
	for key, value := range labels {
		filter = append(filter, key+"="+value)
	}
	raw, _ := json.Marshal(map[string][]string{"label": filter})
	return url.QueryEscape(string(raw))
}

// List finds containers carrying all the labels.
func (d *Docker) List(ctx context.Context, labels map[string]string) ([]Instance, error) {
	var containers []struct {
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/containers/json?all=true&filters="+labelFilter(labels), nil, &containers); err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(containers))
	for _, item := range containers {
		if len(item.Names) == 0 {
			continue
		}
		instances = append(instances, Instance{Name: strings.TrimPrefix(item.Names[0], "/"), Labels: item.Labels, Running: item.State == "running"})
	}
	return instances, nil
}

// ListVolumes finds volumes carrying all the labels.
func (d *Docker) ListVolumes(ctx context.Context, labels map[string]string) ([]VolumeInfo, error) {
	var listing struct {
		Volumes []struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		} `json:"Volumes"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/volumes?filters="+labelFilter(labels), nil, &listing); err != nil {
		return nil, err
	}
	volumes := make([]VolumeInfo, 0, len(listing.Volumes))
	for _, item := range listing.Volumes {
		volumes = append(volumes, VolumeInfo{Name: item.Name, Labels: item.Labels})
	}
	return volumes, nil
}

// Restart kills everything in the container and starts it again. Starting a stopped
// container also goes through here, so the engine's restart covers both.
func (d *Docker) Restart(ctx context.Context, name string) error {
	_, err := d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/restart?t=0", nil, nil)
	return err
}

// Stop kills the container and leaves it and its volume in place.
func (d *Docker) Stop(ctx context.Context, name string) error {
	status, err := d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/kill?signal=KILL", nil, nil)
	if status == http.StatusNotFound || status == http.StatusConflict {
		return nil // gone or already stopped
	}
	return err
}

// Remove kills the container, then removes it. PID 1 ignores SIGTERM, and a forced
// delete on Podman would otherwise wait out its stop timeout, leaving a helper that may
// hold a credential alive for longer than it has to be.
func (d *Docker) Remove(ctx context.Context, name string) error {
	_, _ = d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/kill?signal=KILL", nil, nil)
	status, err := d.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(name)+"?force=true", nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// RemoveVolume removes the volume.
func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	status, err := d.do(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name)+"?force=true", nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// Exec creates and starts an exec over a hijacked connection. Closing the connection
// when ctx ends is what unblocks everything: the sandbox can hold stdout open forever
// through a process that left the exec's process tree, so end of stream is never
// awaited without a deadline.
func (d *Docker) Exec(ctx context.Context, name string, request ExecRequest) (int, error) {
	var created struct {
		ID string `json:"Id"`
	}
	body := map[string]any{
		"AttachStdin":  request.Stdin != nil,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Privileged":   false,
		"User":         fmt.Sprintf("%d:%d", UID, GID),
		"Cmd":          request.Argv,
	}
	if _, err := d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/exec", body, &created); err != nil {
		return 0, err
	}
	if created.ID == "" {
		return 0, errors.New("the engine returned no exec id")
	}
	conn, reader, err := d.startExec(ctx, created.ID, request.Stdin != nil)
	if err != nil {
		return 0, err
	}
	var closeOnce = make(chan struct{})
	stopWatch := make(chan struct{})
	go func() {
		defer close(closeOnce)
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatch:
		}
	}()
	if request.Stdin != nil {
		go func() {
			_, _ = io.Copy(conn, request.Stdin)
			if closer, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = closer.CloseWrite()
			}
		}()
	}
	streamErr := demux(reader, request.Stdout, request.Stderr)
	close(stopWatch)
	<-closeOnce
	_ = conn.Close()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if streamErr != nil {
		return 0, streamErr
	}
	return d.execExitCode(ctx, created.ID)
}

func (d *Docker) startExec(ctx context.Context, id string, withStdin bool) (net.Conn, io.Reader, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("container engine unreachable: %w", err)
	}
	raw, _ := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url("/exec/"+url.PathEscape(id)+"/start"), bytes.NewReader(raw))
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	// Bound only the wait for the response line. The stream itself runs under ctx.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	}
	buffered := bufio.NewReader(conn)
	response, err := http.ReadResponse(buffered, request)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("exec start: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch {
	case response.StatusCode == http.StatusSwitchingProtocols:
		return conn, buffered, nil
	case response.StatusCode == http.StatusOK:
		return conn, response.Body, nil
	}
	defer func() { _ = conn.Close() }()
	message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return nil, nil, fmt.Errorf("exec start returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
}

// demux splits the engine's frames. Frame headers come from the engine and only the
// payload from the sandbox, so a hostile process cannot forge a header.
func demux(reader io.Reader, stdout, stderr io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			if errors.Is(err, io.EOF) || isClosedConn(err) {
				return nil
			}
			return err
		}
		var target io.Writer
		switch header[0] {
		case 1, 0:
			target = stdout
		case 2:
			target = stderr
		default:
			return fmt.Errorf("unknown stream type %d in exec output", header[0])
		}
		if target == nil {
			target = io.Discard
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, err := io.CopyN(target, reader, size); err != nil {
			if isClosedConn(err) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func isClosedConn(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (d *Docker) execExitCode(ctx context.Context, id string) (int, error) {
	// The stream can end a moment before the engine records the exit.
	for attempt := 0; attempt < 50; attempt++ {
		var state struct {
			Running  bool `json:"Running"`
			ExitCode *int `json:"ExitCode"`
		}
		if _, err := d.do(ctx, http.MethodGet, "/exec/"+url.PathEscape(id)+"/json", nil, &state); err != nil {
			return 0, err
		}
		if !state.Running && state.ExitCode != nil {
			return *state.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return 0, errors.New("the engine did not report an exit code for exec " + strconv.Quote(id))
}
