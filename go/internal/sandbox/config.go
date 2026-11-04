package sandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Defaults and hard ceilings. A value above its ceiling refuses to start: a typo
// must not silently produce a bigger or a smaller sandbox than the operator asked for.
const (
	defaultRuntime = "runsc"
	defaultPrefix  = "kionga-sandbox-"

	defaultPids, maxPids                 = int64(512), int64(4096)
	defaultMemoryBytes, maxMemoryBytes   = int64(2 << 30), int64(16 << 30)
	defaultCPUs, maxCPUs                 = 2.0, 16.0
	defaultTmpBytes, maxTmpBytes         = int64(256 << 20), int64(4 << 30)
	defaultDiskMB, maxDiskMB             = int64(10240), int64(512000)
	defaultDiskFiles, maxDiskFiles       = int64(2_000_000), int64(50_000_000)
	defaultDiskCheck, maxDiskCheck       = int64(10), int64(300)
	defaultMinFreeMB, maxMinFreeMB       = int64(5120), int64(10 << 20)
	defaultTTLSeconds, maxTTLSeconds     = 86400, 7 * 86400
	defaultGitTimeout, maxGitTimeout     = 900, 3600
	defaultBundleBytes, maxBundleBytes   = int64(1 << 30), int64(8 << 30)
	defaultMaxSandboxes, maxMaxSandboxes = 256, 4096

	// The exec limits in the contract are not configurable.
	defaultExecTimeout = 120 * time.Second
	maxExecTimeout     = 1800 * time.Second
	outputCap          = 64 << 10

	maxFileRead  = 1 << 20
	maxFileWrite = 16 << 20
	maxCommand   = 128<<10 - 1 // Linux MAX_ARG_STRLEN

	defaultExpectKernel = "gvisor" // found in the uname -r of a gVisor sentry, such as 4.19.0-gvisor
	minTokenLength      = 16
	minProdTokenLength  = 32
	maxCredentialLength = 16 << 10
	gitUsernameDefault  = "x-access-token"
)

// Config is the manager's validated configuration.
type Config struct {
	Token       string
	Driver      string // docker or kubernetes
	Runtime     string
	AllowUnsafe bool
	// ExpectKernel must appear in the kernel release a verified sandbox reports. It proves
	// the runtime really is gVisor, whatever name the engine registered it under.
	ExpectKernel string
	Images       []string
	HelperImage  string
	GitHosts     []string
	GitNetwork   string
	GitUsername  string
	GitCAPEM     []byte
	// GitAllowPrivateForTests lets a helper reach a private git host, for local test
	// servers. LoadConfig refuses it in production.
	GitAllowPrivateForTests bool
	NamePrefix              string
	Pids                    int64
	MemoryBytes             int64
	NanoCPUs                int64
	TmpBytes                int64
	// DiskBytes and DiskFiles are per workspace. Under Docker they are a usage check with
	// a write window; see DiskCheck and the README. Under Kubernetes DiskBytes sizes the claim.
	DiskBytes int64
	DiskFiles int64
	DiskCheck time.Duration
	// MinFreeDiskBytes refuses new sandboxes when the engine's disk is under it.
	MinFreeDiskBytes int64
	BundleBytes      int64
	MaxSandboxes     int64
	TTL              time.Duration
	GitTimeout       time.Duration
	// HelperMaxAge removes a helper left behind by a crashed manager, because it
	// may still hold a credential on its tmpfs.
	HelperMaxAge time.Duration

	Port         string
	DockerSocket string
	DockerAPI    string
	Namespace    string
	StorageClass string
}

// LoadConfig reads and validates the environment. Getenv is a parameter so tests
// do not touch the process environment.
func LoadConfig(getenv func(string) string) (Config, error) {
	config := Config{
		Token:        getenv("KIONGA_SANDBOX_TOKEN"),
		Driver:       orDefault(getenv("KIONGA_SANDBOX_DRIVER"), "docker"),
		Runtime:      orDefault(getenv("KIONGA_SANDBOX_RUNTIME"), defaultRuntime),
		AllowUnsafe:  getenv("KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME") == "1",
		ExpectKernel: orDefault(getenv("KIONGA_SANDBOX_EXPECT_KERNEL"), defaultExpectKernel),
		GitUsername:  orDefault(getenv("KIONGA_SANDBOX_GIT_USERNAME"), gitUsernameDefault),
		NamePrefix:   orDefault(getenv("KIONGA_SANDBOX_NAME_PREFIX"), defaultPrefix),
		Port:         orDefault(getenv("PORT"), "8088"),
		DockerSocket: orDefault(getenv("KIONGA_SANDBOX_DOCKER_SOCKET"), "/var/run/docker.sock"),
		DockerAPI:    getenv("DOCKER_API_VERSION"),
		Namespace:    getenv("KIONGA_SANDBOX_NAMESPACE"),
		StorageClass: getenv("KIONGA_SANDBOX_STORAGE_CLASS"),

		GitAllowPrivateForTests: getenv("KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS") == "1",
	}
	production := getenv("KIONGA_ENVIRONMENT") == "production"
	if config.Token == "" {
		return Config{}, errors.New("KIONGA_SANDBOX_TOKEN is required")
	}
	if len(config.Token) < minTokenLength {
		return Config{}, fmt.Errorf("KIONGA_SANDBOX_TOKEN must be at least %d characters", minTokenLength)
	}
	if production && len(config.Token) < minProdTokenLength {
		return Config{}, fmt.Errorf("production KIONGA_SANDBOX_TOKEN must be at least %d characters", minProdTokenLength)
	}
	if raw := getenv("KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME"); raw != "" && raw != "1" {
		return Config{}, errors.New("KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME must be 1 or unset")
	}
	if config.AllowUnsafe && production {
		return Config{}, errors.New("KIONGA_SANDBOX_ALLOW_UNSAFE_RUNTIME is refused when KIONGA_ENVIRONMENT=production")
	}
	if raw := getenv("KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS"); raw != "" && raw != "1" {
		return Config{}, errors.New("KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS must be 1 or unset")
	}
	if config.GitAllowPrivateForTests && production {
		return Config{}, errors.New("KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS is refused when KIONGA_ENVIRONMENT=production")
	}
	if config.Driver != "docker" && config.Driver != "kubernetes" {
		return Config{}, errors.New("KIONGA_SANDBOX_DRIVER must be docker or kubernetes")
	}
	if !runtimeName.MatchString(config.Runtime) {
		return Config{}, errors.New("KIONGA_SANDBOX_RUNTIME is not a valid runtime name")
	}
	if !namePrefix.MatchString(config.NamePrefix) {
		return Config{}, errors.New("KIONGA_SANDBOX_NAME_PREFIX must be lower case letters, digits and dashes, at most 31 characters")
	}
	if config.Driver == "kubernetes" && config.Namespace == "" {
		return Config{}, errors.New("KIONGA_SANDBOX_NAMESPACE is required with the kubernetes driver")
	}
	if !gitUsername.MatchString(config.GitUsername) {
		return Config{}, errors.New("KIONGA_SANDBOX_GIT_USERNAME is not valid")
	}
	config.GitNetwork = orDefault(getenv("KIONGA_SANDBOX_GIT_NETWORK"), config.NamePrefix+"git")
	if !dockerName.MatchString(config.GitNetwork) {
		return Config{}, errors.New("KIONGA_SANDBOX_GIT_NETWORK is not a valid network name")
	}
	switch config.GitNetwork {
	case "none", "host", "bridge", "default", "podman", "nat":
		return Config{}, fmt.Errorf("KIONGA_SANDBOX_GIT_NETWORK cannot be %s: helpers get a network of their own", config.GitNetwork)
	}
	var err error
	if config.Images, err = parseImages(getenv("KIONGA_SANDBOX_IMAGES"), config.Driver); err != nil {
		return Config{}, err
	}
	config.HelperImage = getenv("KIONGA_SANDBOX_HELPER_IMAGE")
	if config.HelperImage != "" {
		if !imageAllowedShape(config.HelperImage, config.Driver) {
			return Config{}, errors.New("KIONGA_SANDBOX_HELPER_IMAGE must be pinned by digest")
		}
	} else if len(config.Images) > 0 {
		config.HelperImage = config.Images[0]
	}
	if config.GitHosts, err = parseHosts(getenv("KIONGA_SANDBOX_GIT_HOSTS")); err != nil {
		return Config{}, err
	}
	if path := getenv("KIONGA_SANDBOX_GIT_CA_FILE"); path != "" {
		// An operator-set path, never request input.
		raw, err := os.ReadFile(path) // #nosec G304 G703 -- operator configuration
		if err != nil {
			return Config{}, errors.New("cannot read KIONGA_SANDBOX_GIT_CA_FILE")
		}
		if !strings.Contains(string(raw), "BEGIN CERTIFICATE") || len(raw) > 1<<20 {
			return Config{}, errors.New("KIONGA_SANDBOX_GIT_CA_FILE must be a PEM bundle under 1 MiB")
		}
		config.GitCAPEM = raw
	}
	if config.Pids, err = limit(getenv, "KIONGA_SANDBOX_PIDS", defaultPids, maxPids); err != nil {
		return Config{}, err
	}
	if config.MemoryBytes, err = limit(getenv, "KIONGA_SANDBOX_MEMORY_BYTES", defaultMemoryBytes, maxMemoryBytes); err != nil {
		return Config{}, err
	}
	if config.TmpBytes, err = limit(getenv, "KIONGA_SANDBOX_TMP_BYTES", defaultTmpBytes, maxTmpBytes); err != nil {
		return Config{}, err
	}
	diskMB, err := limit(getenv, "KIONGA_SANDBOX_DISK_MB", defaultDiskMB, maxDiskMB)
	if err != nil {
		return Config{}, err
	}
	config.DiskBytes = diskMB << 20
	if config.DiskFiles, err = limit(getenv, "KIONGA_SANDBOX_DISK_FILES", defaultDiskFiles, maxDiskFiles); err != nil {
		return Config{}, err
	}
	diskCheck, err := limit(getenv, "KIONGA_SANDBOX_DISK_CHECK_SECONDS", defaultDiskCheck, maxDiskCheck)
	if err != nil {
		return Config{}, err
	}
	config.DiskCheck = time.Duration(diskCheck) * time.Second
	minFree, err := limit(getenv, "KIONGA_SANDBOX_MIN_FREE_DISK_MB", defaultMinFreeMB, maxMinFreeMB)
	if err != nil {
		return Config{}, err
	}
	config.MinFreeDiskBytes = minFree << 20
	if config.BundleBytes, err = limit(getenv, "KIONGA_SANDBOX_GIT_BUNDLE_BYTES", defaultBundleBytes, maxBundleBytes); err != nil {
		return Config{}, err
	}
	if config.MaxSandboxes, err = limit(getenv, "KIONGA_SANDBOX_MAX_SANDBOXES", int64(defaultMaxSandboxes), int64(maxMaxSandboxes)); err != nil {
		return Config{}, err
	}
	ttl, err := limit(getenv, "KIONGA_SANDBOX_TTL_SECONDS", int64(defaultTTLSeconds), int64(maxTTLSeconds))
	if err != nil {
		return Config{}, err
	}
	config.TTL = time.Duration(ttl) * time.Second
	gitTimeout, err := limit(getenv, "KIONGA_SANDBOX_GIT_TIMEOUT_SECONDS", int64(defaultGitTimeout), int64(maxGitTimeout))
	if err != nil {
		return Config{}, err
	}
	config.GitTimeout = time.Duration(gitTimeout) * time.Second
	config.HelperMaxAge = config.GitTimeout + 5*time.Minute
	cpus := defaultCPUs
	if raw := getenv("KIONGA_SANDBOX_CPUS"); raw != "" {
		cpus, err = strconv.ParseFloat(raw, 64)
		if err != nil || cpus <= 0 || cpus > maxCPUs {
			return Config{}, fmt.Errorf("KIONGA_SANDBOX_CPUS must be above 0 and at most %g", maxCPUs)
		}
	}
	config.NanoCPUs = int64(cpus * 1e9)
	return config, nil
}

var (
	runtimeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	namePrefix  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	gitUsername = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	dockerName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	pinnedImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`)
	imageID     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	hostEntry   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?$`)
)

// imageAllowedShape accepts repository@sha256:digest. The docker driver also takes
// a bare local image ID, which is equally content-addressed but only exists on the
// engine that built it. It cannot be requested by name, only be the default.
func imageAllowedShape(image, driver string) bool {
	return pinnedImage.MatchString(image) || (driver == "docker" && imageID.MatchString(image))
}

func parseImages(raw, driver string) ([]string, error) {
	var images []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !imageAllowedShape(item, driver) {
			return nil, fmt.Errorf("KIONGA_SANDBOX_IMAGES entries must be pinned as repository@sha256:digest, got %q", item)
		}
		images = append(images, item)
	}
	return images, nil
}

func parseHosts(raw string) ([]string, error) {
	var hosts []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if !hostEntry.MatchString(item) {
			return nil, fmt.Errorf("KIONGA_SANDBOX_GIT_HOSTS entry %q must be host or host:port", item)
		}
		if host, port, err := net.SplitHostPort(item); err == nil && (host == "" || port == "") {
			return nil, fmt.Errorf("KIONGA_SANDBOX_GIT_HOSTS entry %q must be host or host:port", item)
		}
		hosts = append(hosts, item)
	}
	return hosts, nil
}

func limit(getenv func(string) string, name string, fallback, ceiling int64) (int64, error) {
	raw := getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	if value > ceiling {
		return 0, fmt.Errorf("%s=%d exceeds the ceiling of %d", name, value, ceiling)
	}
	return value, nil
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
