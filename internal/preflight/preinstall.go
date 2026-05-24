package preflight

// Preinstall runs the install-time preflight gate invoked by `nanokube
// init` before any on-disk side effect (PKI seeding, manifest render,
// state writes). Today this is a thin delegation to the shared Preflight
// gate; init-specific checks (e.g. no existing static pod manifests,
// no existing etcd data dir) land here as they accrue without changing
// the boot path.
func Preinstall(useBackups bool) error {
	return Preflight(useBackups)
}
