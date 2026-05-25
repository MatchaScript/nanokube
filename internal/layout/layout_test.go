package layout_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/MatchaScript/nanokube/internal/layout"
	"github.com/MatchaScript/nanokube/internal/paths"
)

// TestDefault_MatchesPaths pins the new layout package's production
// values to the legacy paths package, byte-for-byte. Deleted together
// with the paths package in the cleanup task.
func TestDefault_MatchesPaths(t *testing.T) {
	l := layout.Default()
	assert.Equal(t, paths.ConfigDir, l.ConfigDir)
	assert.Equal(t, paths.ConfigFile, l.ConfigFile)
	assert.Equal(t, paths.NanoKubeVarDir, l.NanoKubeVarDir)
	assert.Equal(t, paths.StateDir, l.StateDir)
	assert.Equal(t, paths.LastBootFile, l.LastBootFile)
	assert.Equal(t, paths.LastEventFile, l.LastEventFile)
	assert.Equal(t, paths.BackupsDir, l.BackupsDir)
	assert.Equal(t, paths.RestoreMarker, l.RestoreMarker)
	assert.Equal(t, paths.KubernetesDir, l.KubernetesDir)
	assert.Equal(t, paths.PKIDir, l.PKIDir)
	assert.Equal(t, paths.EtcdPKIDir, l.EtcdPKIDir)
	assert.Equal(t, paths.ManifestsDir, l.ManifestsDir)
	assert.Equal(t, paths.KubeAPIServerManifest, l.KubeAPIServerManifest)
	assert.Equal(t, paths.AdminKubeconfig, l.AdminKubeconfig)
	assert.Equal(t, paths.KubeletKubeconfig, l.KubeletKubeconfig)
	assert.Equal(t, paths.CMKubeconfig, l.CMKubeconfig)
	assert.Equal(t, paths.SchedKubeconfig, l.SchedKubeconfig)
	assert.Equal(t, paths.SuperAdminKubeconfig, l.SuperAdminKubeconfig)
	assert.Equal(t, paths.KubeletDir, l.KubeletDir)
	assert.Equal(t, paths.KubeletConfigFile, l.KubeletConfigFile)
	assert.Equal(t, paths.KubeletFlagsEnvFile, l.KubeletFlagsEnvFile)
	assert.Equal(t, paths.EtcdDataDir, l.EtcdDataDir)
}
