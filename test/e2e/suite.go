//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/MatchaScript/nanokube/test/e2etest"
)

const (
	binPath    = "/usr/bin/nanokube"
	kubeconfig = "/etc/kubernetes/admin.conf"
	// Pinned (not /latest/): the latest redirect 404s whenever a release
	// is cut without its assets uploaded yet, which broke CI (2026-07-07).
	flannelURL = "https://github.com/flannel-io/flannel/releases/download/v0.28.7/kube-flannel.yml"
)

// NanokubeE2ESuite drives the full bootstrap → boot → workload → reset
// lifecycle on a single Ubuntu host. State carries between tests;
// methods are named TestNN_Group_Case so testify's lexicographic
// dispatch order preserves the bash suite's ordering.
type NanokubeE2ESuite struct {
	suite.Suite

	repoRoot   string
	binPath    string
	kubeconfig string
	nodeName   string

	dumpRoot   string
	currentDir string
	testStart  time.Time

	keepArtifacts bool
	skipSetup     bool
	prebuilt      bool

	H *e2etest.Helpers
}

// SetupSuite runs once at the start: root check, env, paths, build,
// host provisioning via bash setup.sh.
func (s *NanokubeE2ESuite) SetupSuite() {
	if os.Geteuid() != 0 {
		s.T().Fatal("e2e suite must run as root (try `sudo -E env \"PATH=$PATH\" go test -tags e2e ...`)")
	}

	s.binPath = binPath
	s.kubeconfig = kubeconfig
	s.T().Setenv("KUBECONFIG", s.kubeconfig)

	host, err := os.Hostname()
	s.Require().NoError(err, "os.Hostname")
	s.nodeName = strings.ToLower(host)

	s.keepArtifacts = os.Getenv("NANOKUBE_E2E_KEEP") == "1"
	s.skipSetup = os.Getenv("NANOKUBE_E2E_SKIP_SETUP") == "1"
	s.prebuilt = os.Getenv("NANOKUBE_E2E_PREBUILT") == "1"

	s.repoRoot = findRepoRoot(s.T())
	s.T().Logf("repo root: %s", s.repoRoot)

	s.dumpRoot = filepath.Join(os.TempDir(), fmt.Sprintf("nanokube-e2e-%d", os.Getpid()))
	s.Require().NoError(os.MkdirAll(s.dumpRoot, 0o755))
	s.T().Logf("dump root: %s", s.dumpRoot)

	if !s.prebuilt {
		s.buildAndInstall()
	}

	if !s.skipSetup {
		s.runSetupScript()
	}

	// H is (re)bound to the per-test t in SetupTest; this initial
	// construction is just so helpers using the suite-level t (e.g. in
	// SetupSuite itself, if ever added) don't dereference nil.
	s.H = s.newHelpers()
}

func (s *NanokubeE2ESuite) newHelpers() *e2etest.Helpers {
	return e2etest.New(s.T(), e2etest.Config{
		Bin:        s.binPath,
		Kubeconfig: s.kubeconfig,
		NodeName:   s.nodeName,
		FlannelURL: flannelURL,
	})
}

// TearDownSuite removes the dump root unless a test failed or
// NANOKUBE_E2E_KEEP=1 asked for preservation. nanokube reset is NOT
// called here — the final test (Test16) exercises reset itself, and
// teardown-time reset would mask reset-path failures.
func (s *NanokubeE2ESuite) TearDownSuite() {
	if s.T().Failed() || s.keepArtifacts {
		s.T().Logf("preserving dump root: %s", s.dumpRoot)
		return
	}
	if err := os.RemoveAll(s.dumpRoot); err != nil {
		s.T().Logf("remove dump root %s: %v", s.dumpRoot, err)
	}
}

// SetupTest sets up the per-test diagnostic subdirectory and rebinds
// the helpers to the per-test *testing.T. The rebind is critical:
// testify swaps s.T() for each test method, but a helper captured in
// SetupSuite would keep the suite-level t and any t.Fatalf call from
// inside a test would FailNow the parent suite goroutine (printing
// "subtest may have called FailNow on a parent test") instead of the
// test, suppressing TearDownTest diagnostics.
//
// State is NOT reset between tests — the bash suite carries state
// through, so the Go port follows the same model. TearDownTest
// collects diagnostics only when a test fails.
func (s *NanokubeE2ESuite) SetupTest() {
	s.currentDir = filepath.Join(s.dumpRoot, s.T().Name())
	if err := os.MkdirAll(s.currentDir, 0o755); err != nil {
		s.T().Logf("setup: mkdir %s: %v", s.currentDir, err)
	}
	s.testStart = time.Now()
	s.H = s.newHelpers()
}

// TearDownTest dumps diagnostics on failure and logs test duration.
func (s *NanokubeE2ESuite) TearDownTest() {
	if s.T().Failed() {
		s.H.DumpDiagnostics(s.currentDir)
		s.T().Logf("artifacts: %s", s.currentDir)
	}
	s.T().Logf("test %q done in %s", s.T().Name(), time.Since(s.testStart))
}

func (s *NanokubeE2ESuite) buildAndInstall() {
	s.T().Log("building nanokube binary")
	tmpBin := filepath.Join(s.T().TempDir(), "nanokube")
	cmd := exec.Command("go", "build", "-o", tmpBin, "./cmd/nanokube")
	cmd.Dir = s.repoRoot
	cmd.Stdout = testWriter{s.T()}
	cmd.Stderr = testWriter{s.T()}
	s.Require().NoError(cmd.Run(), "go build")

	install := exec.Command("install", "-m", "0755", tmpBin, s.binPath)
	var out bytes.Buffer
	install.Stdout = &out
	install.Stderr = &out
	s.Require().NoError(install.Run(), "install nanokube: %s", out.String())
}

func (s *NanokubeE2ESuite) runSetupScript() {
	script := filepath.Join(s.repoRoot, "test", "e2e", "setup.sh")
	s.T().Logf("running %s", script)
	cmd := exec.Command("bash", script)
	cmd.Stdout = testWriter{s.T()}
	cmd.Stderr = testWriter{s.T()}
	cmd.Env = os.Environ()
	s.Require().NoError(cmd.Run(), "setup.sh")
}

// findRepoRoot walks up from this source file's directory until it
// finds go.mod. Falls back to the suite's working directory.
func findRepoRoot(t TestingT) string {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate go.mod above " + here)
		}
		dir = parent
	}
}

// TestingT is the minimal testing surface findRepoRoot uses; lets the
// helper stay testable without depending on testify.
type TestingT interface {
	Fatal(args ...any)
}

// testWriter is an io.Writer that forwards each Write to t.Logf,
// streaming subprocess output through the test log rather than
// buffering for failure-time dump.
type testWriter struct {
	t interface{ Logf(string, ...any) }
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
