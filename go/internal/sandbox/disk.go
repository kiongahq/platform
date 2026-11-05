package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

// Disk limits under the Docker driver are a usage check, not a hard limit. The engine's
// local volumes have no size limit on common filesystems, so a writer can pass the limit
// by whatever it writes between two checks. See "Disk" in the README for the window.
// Kubernetes claims are bounded by the storage class and skip all of this.

const (
	meterTimeout   = 60 * time.Second
	freeDiskMaxAge = 15 * time.Second
	sweepWorkers   = 4
	exitUnmeasured = 80
	// meterMemoryBytes bounds a meter walking a hostile tree.
	meterMemoryBytes = 256 << 20
	meterPids        = 64

	// meterScript runs in a throwaway container with the workspace mounted read-only and
	// shares no process namespace with the sandbox, so no agent process can kill it, stop
	// it or write to its stdout. The tree is hostile only as data: du follows no symlinks
	// and opens no FIFOs. A directory hidden with chmod makes du exit nonzero, which is
	// handled as over the limit, because hiding data must not earn more room.
	meterScript = `used=$(du -sxB1 /workspace 2>/dev/null) || exit 80
files=$(du -sx --inodes /workspace 2>/dev/null) || exit 80
free=$(df -B1 --output=avail /workspace | tail -n 1) || exit 81
printf '%s %s %s\n' "${used%%[!0-9]*}" "${files%%[!0-9]*}" "$free"`
)

type diskUsage struct {
	UsedBytes int64
	Files     int64
	FreeBytes int64
}

var errUnmeasurable = errors.New("the workspace could not be measured")

func (s *Service) diskEnforced() bool { return s.driver.Name() == "docker" }

func quotaExceeded(message string) error {
	return &client.Error{Status: http.StatusInsufficientStorage, Code: client.CodeQuotaExceeded, Message: message}
}

// roView starts a throwaway container with the volume mounted read-only. Callers defer
// the cleanup. It has no network and no credentials, and writes nothing to the workspace.
func (s *Service) roView(ctx context.Context, id, volume string) (string, func(), error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", func() {}, err
	}
	name := s.config.NamePrefix + "meter-" + hex.EncodeToString(suffix)
	spec := s.hardenedSpec(name, s.config.HelperImage, RoleProbe)
	if id != "" {
		spec.Labels[LabelID] = id
	}
	spec.MemoryBytes, spec.Pids = meterMemoryBytes, meterPids
	spec.Volume = &VolumeMount{Name: volume, Path: WorkspacePath, ReadOnly: true}
	cleanup := func() { s.removeQuietly(name) }
	if err := s.driver.Create(ctx, spec); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return name, cleanup, nil
}

// measure reads a volume from outside the sandbox. errUnmeasurable means the contents
// prevented it. Any other error is the engine's, and says nothing about the workspace.
func (s *Service) measure(parent context.Context, id, volume string) (diskUsage, error) {
	ctx, cancel := context.WithTimeout(parent, meterTimeout)
	defer cancel()
	name, cleanup, err := s.roView(ctx, id, volume)
	if err != nil {
		return diskUsage{}, err
	}
	defer cleanup()
	stdout := &headBuffer{limit: 256}
	code, err := s.driver.Exec(ctx, name, ExecRequest{Argv: []string{"/usr/bin/env", "-i", safePath, "LC_ALL=C", "/bin/sh", "-c", meterScript}, Stdout: stdout, Stderr: io.Discard})
	if err != nil {
		if ctx.Err() != nil && parent.Err() == nil {
			return diskUsage{}, errUnmeasurable // a tree too large to walk in time
		}
		return diskUsage{}, err
	}
	if code == exitUnmeasured {
		return diskUsage{}, errUnmeasurable
	}
	fields := strings.Fields(string(stdout.data))
	if code != 0 || len(fields) != 3 {
		return diskUsage{}, fmt.Errorf("disk meter exited %d with %d fields", code, len(fields))
	}
	values := make([]int64, 3)
	for index, field := range fields {
		if values[index], err = strconv.ParseInt(field, 10, 64); err != nil || values[index] < 0 {
			return diskUsage{}, fmt.Errorf("disk meter returned an unreadable value")
		}
	}
	s.recordFree(values[2])
	return diskUsage{UsedBytes: values[0], Files: values[1], FreeBytes: values[2]}, nil
}

func (s *Service) overLimit(usage diskUsage) string {
	if usage.UsedBytes > s.config.DiskBytes {
		return fmt.Sprintf("the workspace uses %d MiB, over the limit of %d MiB", usage.UsedBytes>>20, s.config.DiskBytes>>20)
	}
	if usage.Files > s.config.DiskFiles {
		return fmt.Sprintf("the workspace holds %d files, over the limit of %d", usage.Files, s.config.DiskFiles)
	}
	return ""
}

// enforceDisk measures one sandbox and stops it if it is over. The stop is the point: a
// process still writing must end, and the container staying stopped is the record, since
// a later call measures again and refuses again. The volume is kept so files stay readable.
func (s *Service) enforceDisk(ctx context.Context, id string, found Instance) error {
	if !s.diskEnforced() {
		return nil
	}
	usage, err := s.measure(ctx, id, s.volumeName(id))
	reason := ""
	switch {
	case errors.Is(err, errUnmeasurable):
		reason = "the workspace could not be measured, for example because a directory is unreadable, so it is treated as over the limit"
	case err != nil:
		s.logger.Printf("sandbox: disk check of %s failed: %v", id, err)
		return unavailable("the workspace could not be checked against its disk limit")
	default:
		reason = s.overLimit(usage)
	}
	if reason == "" {
		s.quotaStopped.Delete(found.Name)
		return nil
	}
	s.logger.Printf("sandbox: stopping %s: %s", id, reason)
	s.quotaStopped.Store(found.Name, reason)
	if found.Running {
		stopCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := s.driver.Stop(stopCtx, found.Name); err != nil {
			s.logger.Printf("sandbox: could not stop %s: %v", id, err)
		}
	}
	return quotaExceeded(reason)
}

// quotaError returns the refusal if this sandbox was stopped for disk during a call.
func (s *Service) quotaError(name string) error {
	if reason, ok := s.quotaStopped.Load(name); ok {
		return quotaExceeded(reason.(string))
	}
	return nil
}

// SweepDisk checks every running sandbox. A cycle takes a meter's start time per
// sandbox, divided by the workers, and that time widens the write window.
func (s *Service) SweepDisk(ctx context.Context) {
	if !s.diskEnforced() {
		return
	}
	instances, err := s.driver.List(ctx, s.roleLabels(RoleSandbox))
	if err != nil {
		s.logger.Printf("sandbox: disk sweep could not list: %v", err)
		return
	}
	slots := make(chan struct{}, sweepWorkers)
	var waiting sync.WaitGroup
	for _, item := range instances {
		id := item.Labels[LabelID]
		if !item.Running || !sandboxID.MatchString(id) {
			continue
		}
		waiting.Add(1)
		slots <- struct{}{}
		go func(item Instance, id string) {
			defer waiting.Done()
			defer func() { <-slots }()
			_ = s.enforceDisk(ctx, id, item)
		}(item, id)
	}
	waiting.Wait()
}

func (s *Service) recordFree(free int64) {
	s.freeMu.Lock()
	s.freeBytes, s.freeCheckedAt = free, s.now()
	s.freeMu.Unlock()
}

// checkFreeDisk is the floor under all sandboxes. The manager has no view of the host's
// disks, so it reads the free space of a filesystem holding engine volumes, from a meter
// on a small volume of its own. A recent reading from any meter counts.
func (s *Service) checkFreeDisk(ctx context.Context) error {
	if !s.diskEnforced() {
		return nil
	}
	s.freeMu.Lock()
	free, checked := s.freeBytes, s.freeCheckedAt
	s.freeMu.Unlock()
	if checked.IsZero() || s.now().Sub(checked) > freeDiskMaxAge {
		usage, err := s.measure(ctx, "", s.config.NamePrefix+"disk-floor")
		if err != nil {
			s.logger.Printf("sandbox: free disk check failed: %v", err)
			return unavailable("the engine's free disk could not be checked")
		}
		free = usage.FreeBytes
	}
	if free < s.config.MinFreeDiskBytes {
		s.logger.Printf("sandbox: refusing a new sandbox: %d MiB free, floor %d MiB", free>>20, s.config.MinFreeDiskBytes>>20)
		return unavailable("the engine has too little free disk for a new sandbox")
	}
	return nil
}

// readable returns where to run a read-only operation. A running sandbox serves it. A
// stopped one is not restarted, because that would let an over-limit sandbox run again,
// so a throwaway read-only container serves it instead.
func (s *Service) readable(ctx context.Context, id string) (string, func(), error) {
	found, err := s.instance(ctx, id)
	if err != nil {
		return "", func() {}, err
	}
	if found.Running {
		return found.Name, func() {}, nil
	}
	name, cleanup, err := s.roView(ctx, id, s.volumeName(id))
	if err != nil {
		s.logger.Printf("sandbox: read-only view of %s failed: %v", id, err)
		return "", func() {}, unavailable("the workspace could not be opened read-only")
	}
	if err := s.attest(ctx, name); err != nil {
		cleanup()
		s.logger.Printf("sandbox: read-only view of %s failed verification: %v", id, err)
		return "", func() {}, unavailable("the workspace could not be opened read-only")
	}
	return name, cleanup, nil
}

// writable is the gate for anything that can run agent code or write: it measures first,
// then recovers a stopped sandbox only if it is under its limit.
func (s *Service) writable(ctx context.Context, id string) (Instance, error) {
	found, err := s.instance(ctx, id)
	if err != nil {
		return Instance{}, err
	}
	if err := s.enforceDisk(ctx, id, found); err != nil {
		return Instance{}, err
	}
	return s.running(ctx, id)
}
