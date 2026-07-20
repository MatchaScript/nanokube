package render

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	utilsnet "k8s.io/utils/net"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/images"
)

// This file builds the four control-plane static pod manifests
// (kube-apiserver, kube-controller-manager, kube-scheduler, etcd) as
// typed corev1.Pod values entirely in nanokube's own code.
// IMPLEMENTATION_PLAN.md §1.2 (2026-07-13 ruling): "static pod
// manifest の生成には kubeadm のコードを使用せず、オペレーターの自前
// テンプレートでレンダリングする". kubeadm's phase functions
// (cmd/kubeadm/app/phases/controlplane, .../phases/etcd) that used to do
// this are no longer called anywhere in non-test code; their construction
// logic (arg lists, volume/mount tables, probe timings) is mirrored here
// from a reader survey of kubeadm v1.35.0's source, byte-verified against
// kubeadm's own output for a config matrix (golden_test.go); a
// build-tagged parity test (parity_test.go, kubeadm_parity) cross-checks
// this construction against a live kubeadm call as the ongoing
// development-time reference §1.3 calls for — never run by default `go
// test`.
//
// Still legitimately reused from kubeadm: the public InitConfiguration/
// ClusterConfiguration/Arg/EnvVar types (this is the render package's
// existing input surface, unrelated to how manifests get built from it),
// kubeadmconstants for file/volume names, ports and the etcd version
// table, and cmd/kubeadm/app/images for image string construction —
// §1.2's table marks version-table reference and public types as
// explicitly in-scope ("バージョン簿記の自動化が kubeadm を利用する動機の
// 本体").
//
// Two deviations from literal kubeadm parity, both deliberate:
//
//  1. External etcd. ControlPlaneManifests (render.go) rejects
//     cfg.ClusterConfiguration.Etcd.External != nil up front, mirroring
//     the guard kubeadm's own etcd.CreateLocalEtcdStaticPodManifestFile
//     has always enforced here (nanokube never plumbs a --config path to
//     reach this render function with External set). Because of that
//     guard, kube-apiserver's etcd wiring below only ever implements the
//     "local etcd" branch of kubeadm's getAPIServerCommand — the
//     External-etcd branch (and the cert-volume dedup logic it drives in
//     getHostPathVolumesForTheControlPlane) is unreachable in the
//     kubeadm-based implementation this replaces, so it is not
//     reproduced.
//
//  2. certphase.UsingExternalCA is not called. This is a third ambient
//     host read the reader survey that fed this task didn't flag (it
//     only found the proxy-env burn-in and the CA-trust os.Stat loop):
//     kubeadm's getControllerManagerCommand calls UsingExternalCA(cfg),
//     which os.Stat's cfg.CertificatesDir *on whatever machine is
//     rendering* to decide whether to blank out
//     cluster-signing-{cert,key}-file. nanokube always owns and generates
//     its CA itself (internal/certs, materialized under credsDir, never
//     under the render host's real cfg.CertificatesDir/nodePKIDir path),
//     so "using an external CA without its key" cannot happen in this
//     design — the fixed, correct input is to always take the
//     kubeadm-owns-the-CA branch (never blank those two flags), which is
//     also what UsingExternalCA returns in practice on any host that
//     hasn't happened to have real cluster PKI sitting at
//     /etc/kubernetes/pki (verified empirically against the golden
//     capture host before this file was written).

// ambientCACertMountPaths mirrors kubeadm's own unexported
// controlplane.caCertsExtraVolumePaths (volumes.go): host paths that
// kubeadm's GetStaticPodSpecs conditionally mounts into apiserver/
// controller-manager if present ON THE RENDER HOST (an os.Stat check).
// That makes the manifest bytes depend on which of these happen to
// exist on whatever machine runs the render — ineligible for a
// content-hashed desired document (IMPLEMENTATION_PLAN §1.3). nanokube
// carries its own PKI end-to-end via the desired document (Task 4), so
// these distro CA-trust-store mounts serve no purpose here. Unlike the
// pre-T1 implementation, this construction never generates them in the
// first place (no generate-then-strip pass is needed); the list is kept
// only as a regression-guard reference for
// TestControlPlaneManifests_NoAmbientCACertMounts and the parity test.
// The always-present "/etc/ssl/certs" mount kubeadm also adds is NOT in
// this list: unlike these five, it is added unconditionally (not
// os.Stat-gated), so it doesn't vary by render host and this
// construction does emit it (see caCertsVolumeName/caCertsVolumePath).
//
// Tradeoff, deliberate: this leaves apiserver/controller-manager with
// no path to system/public CA trust at all, only nanokube's own PKI.
// Nothing in nanokube's current scope needs it (no OIDC issuer,
// webhook backend, or cloud API signed by a public CA); a future
// feature that does need it must supply CA material via nanokube's own
// PKI/desired document, not rely on the host trust store.
var ambientCACertMountPaths = []string{
	"/etc/pki/ca-trust",
	"/etc/pki/tls/certs",
	"/etc/ca-certificates",
	"/usr/share/ca-certificates",
	"/usr/local/share/ca-certificates",
}

// Fixed volume/mount names and paths kubeadm's controlplane/volumes.go
// and phases/etcd/local.go define as unexported constants. Reproduced
// here verbatim since manifest construction is now this package's own.
const (
	caCertsVolumeName          = "ca-certs"
	caCertsVolumePath          = "/etc/ssl/certs"
	flexvolumeDirVolumeName    = "flexvolume-dir"
	defaultFlexvolumeDirVolume = "/usr/libexec/kubernetes/kubelet-plugins/volume/exec"
	etcdDataVolumeName         = "etcd-data"
	etcdCertsVolumeName        = "etcd-certs"
)

// buildControlPlanePods returns the four control-plane static pods for
// cfg, keyed by kubeadm's own component names ("etcd",
// "kube-apiserver", "kube-controller-manager", "kube-scheduler") so
// callers can pair each with its manifest filename.
func buildControlPlanePods(cfg *kubeadmapi.InitConfiguration) map[string]corev1.Pod {
	return map[string]corev1.Pod{
		kubeadmconstants.Etcd:                  buildEtcdPod(cfg),
		kubeadmconstants.KubeAPIServer:         buildAPIServerPod(cfg),
		kubeadmconstants.KubeControllerManager: buildControllerManagerPod(cfg),
		kubeadmconstants.KubeScheduler:         buildSchedulerPod(cfg),
	}
}

// marshalPodYAML reproduces kubeadm's own encode chain
// (kubeadmutil.MarshalToYaml → MarshalToYamlForCodecs, util/marshal.go)
// exactly, using client-go's own scheme rather than kubeadm's wrapper:
// the YAML-mode json.Serializer registered for corev1 in
// k8s.io/client-go/kubernetes/scheme, run through EncoderForVersion at
// corev1.SchemeGroupVersion. This is the same encoder kubeadm's
// staticpodutil.WriteStaticPodToDisk used, so byte format (field
// ordering from struct tags, null/omitempty handling, YAML style) is
// unchanged from what the manifests looked like before T1.
func marshalPodYAML(pod *corev1.Pod) ([]byte, error) {
	info, ok := runtime.SerializerInfoForMediaType(clientsetscheme.Codecs.SupportedMediaTypes(), runtime.ContentTypeYAML)
	if !ok {
		return nil, fmt.Errorf("render: no %s serializer registered", runtime.ContentTypeYAML)
	}
	encoder := clientsetscheme.Codecs.EncoderForVersion(info.Serializer, corev1.SchemeGroupVersion)
	return runtime.Encode(encoder, pod)
}

// --- shared pod-construction primitives (mirrors util/staticpod) ---

func componentPod(container corev1.Container, volumes map[string]corev1.Volume, annotations map[string]string) corev1.Pod {
	priority := int32(2000001000)
	return corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        container.Name,
			Namespace:   metav1.NamespaceSystem,
			Labels:      map[string]string{"component": container.Name, "tier": kubeadmconstants.ControlPlaneTier},
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			Containers:        []corev1.Container{container},
			Priority:          &priority,
			PriorityClassName: "system-node-critical",
			HostNetwork:       true,
			Volumes:           sortedVolumes(volumes),
			SecurityContext: &corev1.PodSecurityContext{
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		},
	}
}

func componentResources(cpu string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)},
	}
}

func newVolume(name, hostPath string, pathType corev1.HostPathType) corev1.Volume {
	pt := pathType
	return corev1.Volume{
		Name:         name,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostPath, Type: &pt}},
	}
}

func newVolumeMount(name, path string, readOnly bool) corev1.VolumeMount {
	return corev1.VolumeMount{Name: name, MountPath: path, ReadOnly: readOnly}
}

func sortedVolumes(m map[string]corev1.Volume) []corev1.Volume {
	v := make([]corev1.Volume, 0, len(m))
	for _, x := range m {
		v = append(v, x)
	}
	sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	return v
}

func sortedVolumeMounts(m map[string]corev1.VolumeMount) []corev1.VolumeMount {
	v := make([]corev1.VolumeMount, 0, len(m))
	for _, x := range m {
		v = append(v, x)
	}
	sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	return v
}

// hostPathMounts accumulates a component's hostPath volumes/mounts by
// name, so a later add() with a name already present overwrites it —
// this is how kubeadm lets ExtraVolumes entries replace a default mount
// that happens to share their Name (controlplane/volumes.go's
// controlPlaneHostPathMounts).
type hostPathMounts struct {
	volumes map[string]corev1.Volume
	mounts  map[string]corev1.VolumeMount
}

func newHostPathMounts() *hostPathMounts {
	return &hostPathMounts{volumes: map[string]corev1.Volume{}, mounts: map[string]corev1.VolumeMount{}}
}

func (h *hostPathMounts) add(name, hostPath, containerPath string, readOnly bool, pathType corev1.HostPathType) {
	h.volumes[name] = newVolume(name, hostPath, pathType)
	h.mounts[name] = newVolumeMount(name, containerPath, readOnly)
}

func (h *hostPathMounts) addExtra(extra []kubeadmapi.HostPathMount) {
	for _, e := range extra {
		h.add(e.Name, e.HostPath, e.MountPath, e.ReadOnly, e.PathType)
	}
}

func (h *hostPathMounts) mountsSlice() []corev1.VolumeMount { return sortedVolumeMounts(h.mounts) }

// --- arg/env merge helpers (mirrors util/arguments.go, util/env.go) ---

// argumentsToCommand merges base and overrides the way kubeadm's
// ArgumentsToCommand does: an override fully replaces every base entry
// of the same Name (not merged per-instance), remaining args are sorted
// by (Name, Value) so the final command is independent of either
// slice's construction order, then formatted as "--name=value".
func argumentsToCommand(base, overrides []kubeadmapi.Arg) []string {
	args := make([]kubeadmapi.Arg, len(overrides))
	copy(args, overrides)

	overridden := make(map[string]bool, len(overrides))
	for _, a := range overrides {
		overridden[a.Name] = true
	}
	for _, a := range base {
		if !overridden[a.Name] {
			args = append(args, a)
		}
	}

	sort.Slice(args, func(i, j int) bool {
		if args[i].Name == args[j].Name {
			return args[i].Value < args[j].Value
		}
		return args[i].Name < args[j].Name
	})

	command := make([]string, 0, len(args))
	for _, a := range args {
		command = append(command, fmt.Sprintf("--%s=%s", a.Name, a.Value))
	}
	return command
}

// argValue returns the value of the last (highest-index) entry named
// name in args, mirroring kubeadmapi.GetArgValue(args, name, -1).
func argValue(args []kubeadmapi.Arg, name string) (string, bool) {
	for i := len(args) - 1; i >= 0; i-- {
		if args[i].Name == name {
			return args[i].Value, true
		}
	}
	return "", false
}

// setArg updates the last entry named name in args in place, or
// appends a new one if absent — kubeadmapi.SetArgValues(args, name,
// value, 1)'s behavior for the single-occurrence base lists built here.
func setArg(args []kubeadmapi.Arg, name, value string) []kubeadmapi.Arg {
	for i := len(args) - 1; i >= 0; i-- {
		if args[i].Name == name {
			args[i].Value = value
			return args
		}
	}
	return append(args, kubeadmapi.Arg{Name: name, Value: value})
}

// mergeEnvVars converts and dedupes envs by Name (last occurrence
// wins) and sorts by Name — kubeadmutil.MergeKubeadmEnvVars with the
// (always-empty, per T1's ambient-proxy-env removal) proxyEnvs list
// dropped, since it never contributes anything once fixed to empty.
func mergeEnvVars(envs []kubeadmapi.EnvVar) []corev1.EnvVar {
	m := make(map[string]corev1.EnvVar, len(envs))
	for _, e := range envs {
		m[e.Name] = e.EnvVar
	}
	merged := make([]corev1.EnvVar, 0, len(m))
	for _, v := range m {
		merged = append(merged, v)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged
}

// --- probes (mirrors util/staticpod's LivenessProbe/ReadinessProbe/StartupProbe) ---

func createHTTPProbe(host, path, port string, scheme corev1.URIScheme, initialDelaySeconds, timeoutSeconds, failureThreshold, periodSeconds int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Host: host, Path: path, Port: intstr.FromString(port), Scheme: scheme},
		},
		InitialDelaySeconds: initialDelaySeconds,
		TimeoutSeconds:      timeoutSeconds,
		FailureThreshold:    failureThreshold,
		PeriodSeconds:       periodSeconds,
	}
}

func livenessProbe(host, path, port string, scheme corev1.URIScheme) *corev1.Probe {
	return createHTTPProbe(host, path, port, scheme, 10, 15, 8, 10)
}

func readinessProbe(host, path, port string, scheme corev1.URIScheme) *corev1.Probe {
	return createHTTPProbe(host, path, port, scheme, 0, 15, 3, 1)
}

// startupProbe's failureThreshold is derived from timeout (the node's
// own cfg.Timeouts.ControlPlaneComponentHealthCheck — see
// controlPlaneHealthCheckTimeout — falling back to kubeadm's 4-minute
// constant when unset), never from kubeadm's process-global
// GetActiveTimeouts() singleton: reading the value straight off the
// input cfg avoids depending on whichever other kubeadm code path last
// called SetActiveTimeouts in this process, keeping the render fully a
// function of cfg (IMPLEMENTATION_PLAN §1.3 input discipline).
func startupProbe(host, path, port string, scheme corev1.URIScheme, timeout *metav1.Duration) *corev1.Probe {
	const periodSeconds = int32(10)
	timeoutForControlPlaneSeconds := kubeadmconstants.ControlPlaneComponentHealthCheckTimeout.Seconds()
	if timeout != nil {
		timeoutForControlPlaneSeconds = timeout.Duration.Seconds()
	}
	failureThreshold := int32(math.Ceil(timeoutForControlPlaneSeconds / float64(periodSeconds)))
	return createHTTPProbe(host, path, port, scheme, periodSeconds, 15, failureThreshold, periodSeconds)
}

func controlPlaneHealthCheckTimeout(cfg *kubeadmapi.InitConfiguration) *metav1.Duration {
	if cfg.Timeouts != nil {
		return cfg.Timeouts.ControlPlaneComponentHealthCheck
	}
	return nil
}

// getProbeAddress returns addr unless it's an unspecified bind address
// ("0.0.0.0"/"::"), in which case it returns "" so the kubelet probes
// the pod IP instead (kubeadm util/staticpod.go's getProbeAddress —
// see https://github.com/kubernetes/kubeadm/issues/86504 in that
// source for why: an unspecified address is not itself dialable).
func getProbeAddress(addr string) string {
	if addr == "0.0.0.0" || addr == "::" {
		return ""
	}
	return addr
}

func apiServerProbeAddress(endpoint *kubeadmapi.APIEndpoint) string {
	if endpoint != nil && endpoint.AdvertiseAddress != "" {
		return getProbeAddress(endpoint.AdvertiseAddress)
	}
	return "127.0.0.1"
}

func controllerManagerProbeAddress(cc *kubeadmapi.ClusterConfiguration) string {
	if v, ok := argValue(cc.ControllerManager.ExtraArgs, "bind-address"); ok {
		return getProbeAddress(v)
	}
	return "127.0.0.1"
}

func schedulerProbeAddress(cc *kubeadmapi.ClusterConfiguration) string {
	if v, ok := argValue(cc.Scheduler.ExtraArgs, "bind-address"); ok {
		return getProbeAddress(v)
	}
	return "127.0.0.1"
}

// etcdProbeEndpoint mirrors staticpodutil.GetEtcdProbeEndpoint: defaults
// to loopback/EtcdMetricsPort/HTTP, unless Etcd.Local.ExtraArgs sets
// listen-metrics-urls, in which case the first comma-separated URL's
// scheme/host/port are parsed out (falling back to the default piece on
// any parse error or missing component).
func etcdProbeEndpoint(cc *kubeadmapi.ClusterConfiguration, isIPv6 bool) (string, int32, corev1.URIScheme) {
	localhost := "127.0.0.1"
	if isIPv6 {
		localhost = "::1"
	}
	if cc.Etcd.Local == nil || cc.Etcd.Local.ExtraArgs == nil {
		return localhost, kubeadmconstants.EtcdMetricsPort, corev1.URISchemeHTTP
	}
	v, ok := argValue(cc.Etcd.Local.ExtraArgs, "listen-metrics-urls")
	if !ok {
		return localhost, kubeadmconstants.EtcdMetricsPort, corev1.URISchemeHTTP
	}
	v = strings.Split(v, ",")[0]
	parsed, err := url.Parse(v)
	if err != nil {
		return localhost, kubeadmconstants.EtcdMetricsPort, corev1.URISchemeHTTP
	}
	scheme := corev1.URISchemeHTTP
	if parsed.Scheme == "https" {
		scheme = corev1.URISchemeHTTPS
	}
	host := parsed.Hostname()
	if host == "" {
		host = localhost
	}
	port := int32(kubeadmconstants.EtcdMetricsPort)
	if ps := parsed.Port(); ps != "" {
		if p, err := strconv.Atoi(ps); err == nil && p >= 1 && p <= 65535 {
			port = int32(p)
		}
	}
	return host, port, scheme
}

// --- kube-apiserver ---

func apiServerVolumes(cc *kubeadmapi.ClusterConfiguration) *hostPathMounts {
	m := newHostPathMounts()
	m.add(kubeadmconstants.KubeCertificatesVolumeName, cc.CertificatesDir, cc.CertificatesDir, true, corev1.HostPathDirectoryOrCreate)
	m.add(caCertsVolumeName, caCertsVolumePath, caCertsVolumePath, true, corev1.HostPathDirectoryOrCreate)
	m.addExtra(cc.APIServer.ExtraVolumes)
	return m
}

// apiServerCommand mirrors kubeadm's getAPIServerCommand. Etcd wiring
// only implements the local-etcd branch (see the file doc comment,
// deviation 1). authorization-mode: kubeadm computes a filtered default
// via getAuthzModes only to have it discarded whenever the caller
// already supplied authorization-mode in ExtraArgs (an override always
// replaces the base entry of the same Name wholesale, unfiltered) — the
// filtering is therefore unobservable in the final command and is not
// reproduced; the only externally visible behavior is "add
// authorization-mode=Node,RBAC to the base args unless
// authorization-config is present", which is what's implemented here.
func apiServerCommand(cc *kubeadmapi.ClusterConfiguration, endpoint *kubeadmapi.APIEndpoint) []string {
	args := []kubeadmapi.Arg{
		{Name: "advertise-address", Value: endpoint.AdvertiseAddress},
		{Name: "enable-admission-plugins", Value: "NodeRestriction"},
		{Name: "service-cluster-ip-range", Value: cc.Networking.ServiceSubnet},
		{Name: "service-account-key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.ServiceAccountPublicKeyName)},
		{Name: "service-account-signing-key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.ServiceAccountPrivateKeyName)},
		{Name: "service-account-issuer", Value: fmt.Sprintf("https://kubernetes.default.svc.%s", cc.Networking.DNSDomain)},
		{Name: "client-ca-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.CACertName)},
		{Name: "tls-cert-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.APIServerCertName)},
		{Name: "tls-private-key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.APIServerKeyName)},
		{Name: "kubelet-client-certificate", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.APIServerKubeletClientCertName)},
		{Name: "kubelet-client-key", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.APIServerKubeletClientKeyName)},
		{Name: "enable-bootstrap-token-auth", Value: "true"},
		{Name: "secure-port", Value: strconv.Itoa(int(endpoint.BindPort))},
		{Name: "allow-privileged", Value: "true"},
		{Name: "kubelet-preferred-address-types", Value: "InternalIP,ExternalIP,Hostname"},
		{Name: "requestheader-username-headers", Value: "X-Remote-User"},
		{Name: "requestheader-group-headers", Value: "X-Remote-Group"},
		{Name: "requestheader-extra-headers-prefix", Value: "X-Remote-Extra-"},
		{Name: "requestheader-client-ca-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.FrontProxyCACertName)},
		{Name: "requestheader-allowed-names", Value: "front-proxy-client"},
		{Name: "proxy-client-cert-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.FrontProxyClientCertName)},
		{Name: "proxy-client-key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.FrontProxyClientKeyName)},
	}

	etcdLocalhost := "127.0.0.1"
	if utilsnet.IsIPv6String(endpoint.AdvertiseAddress) {
		etcdLocalhost = "::1"
	}
	args = append(args,
		kubeadmapi.Arg{Name: "etcd-servers", Value: "https://" + net.JoinHostPort(etcdLocalhost, strconv.Itoa(kubeadmconstants.EtcdListenClientPort))},
		kubeadmapi.Arg{Name: "etcd-cafile", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.EtcdCACertName)},
		kubeadmapi.Arg{Name: "etcd-certfile", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.APIServerEtcdClientCertName)},
		kubeadmapi.Arg{Name: "etcd-keyfile", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.APIServerEtcdClientKeyName)},
	)
	if cc.Etcd.Local != nil {
		if v, ok := argValue(cc.Etcd.Local.ExtraArgs, "advertise-client-urls"); ok {
			args = setArg(args, "etcd-servers", v)
		}
	}

	if _, ok := argValue(cc.APIServer.ExtraArgs, "authorization-config"); !ok {
		args = append(args, kubeadmapi.Arg{Name: "authorization-mode", Value: "Node,RBAC"})
	}

	return append([]string{"kube-apiserver"}, argumentsToCommand(args, cc.APIServer.ExtraArgs)...)
}

func buildAPIServerPod(cfg *kubeadmapi.InitConfiguration) corev1.Pod {
	cc := &cfg.ClusterConfiguration
	endpoint := &cfg.LocalAPIEndpoint
	mounts := apiServerVolumes(cc)

	container := corev1.Container{
		Name:            kubeadmconstants.KubeAPIServer,
		Image:           images.GetKubernetesImage(kubeadmconstants.KubeAPIServer, cc),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         apiServerCommand(cc, endpoint),
		VolumeMounts:    mounts.mountsSlice(),
		LivenessProbe:   livenessProbe(apiServerProbeAddress(endpoint), "/livez", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS),
		ReadinessProbe:  readinessProbe(apiServerProbeAddress(endpoint), "/readyz", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS),
		StartupProbe:    startupProbe(apiServerProbeAddress(endpoint), "/livez", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS, controlPlaneHealthCheckTimeout(cfg)),
		Resources:       componentResources("250m"),
		Env:             mergeEnvVars(cc.APIServer.ExtraEnvs),
		Ports: []corev1.ContainerPort{
			{Name: kubeadmconstants.ProbePort, ContainerPort: endpoint.BindPort, Protocol: corev1.ProtocolTCP},
		},
	}
	return componentPod(container, mounts.volumes, map[string]string{
		kubeadmconstants.KubeAPIServerAdvertiseAddressEndpointAnnotationKey: endpoint.String(),
	})
}

// --- kube-controller-manager ---

func controllerManagerVolumes(cc *kubeadmapi.ClusterConfiguration) *hostPathMounts {
	m := newHostPathMounts()
	m.add(kubeadmconstants.KubeCertificatesVolumeName, cc.CertificatesDir, cc.CertificatesDir, true, corev1.HostPathDirectoryOrCreate)
	m.add(caCertsVolumeName, caCertsVolumePath, caCertsVolumePath, true, corev1.HostPathDirectoryOrCreate)
	kubeconfig := filepath.Join(kubeadmconstants.KubernetesDir, kubeadmconstants.ControllerManagerKubeConfigFileName)
	m.add(kubeadmconstants.KubeConfigVolumeName, kubeconfig, kubeconfig, true, corev1.HostPathFileOrCreate)
	flexPath := defaultFlexvolumeDirVolume
	if v, ok := argValue(cc.ControllerManager.ExtraArgs, "flex-volume-plugin-dir"); ok {
		flexPath = v
	}
	m.add(flexvolumeDirVolumeName, flexPath, flexPath, false, corev1.HostPathDirectoryOrCreate)
	m.addExtra(cc.ControllerManager.ExtraVolumes)
	return m
}

// controllerManagerCommand mirrors kubeadm's getControllerManagerCommand,
// minus the UsingExternalCA branch (file doc comment, deviation 2):
// cluster-signing-{cert,key}-file are always populated from nanokube's
// own CA.
func controllerManagerCommand(cc *kubeadmapi.ClusterConfiguration) []string {
	kubeconfig := filepath.Join(kubeadmconstants.KubernetesDir, kubeadmconstants.ControllerManagerKubeConfigFileName)
	caFile := filepath.Join(cc.CertificatesDir, kubeadmconstants.CACertName)

	args := []kubeadmapi.Arg{
		{Name: "bind-address", Value: "127.0.0.1"},
		{Name: "leader-elect", Value: "true"},
		{Name: "kubeconfig", Value: kubeconfig},
		{Name: "authentication-kubeconfig", Value: kubeconfig},
		{Name: "authorization-kubeconfig", Value: kubeconfig},
		{Name: "client-ca-file", Value: caFile},
		{Name: "requestheader-client-ca-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.FrontProxyCACertName)},
		{Name: "root-ca-file", Value: caFile},
		{Name: "service-account-private-key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.ServiceAccountPrivateKeyName)},
		{Name: "cluster-signing-cert-file", Value: caFile},
		{Name: "cluster-signing-key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.CAKeyName)},
		{Name: "use-service-account-credentials", Value: "true"},
		{Name: "controllers", Value: "*,bootstrapsigner,tokencleaner"},
	}

	if cc.Networking.PodSubnet != "" {
		args = append(args,
			kubeadmapi.Arg{Name: "allocate-node-cidrs", Value: "true"},
			kubeadmapi.Arg{Name: "cluster-cidr", Value: cc.Networking.PodSubnet},
		)
		if cc.Networking.ServiceSubnet != "" {
			args = append(args, kubeadmapi.Arg{Name: "service-cluster-ip-range", Value: cc.Networking.ServiceSubnet})
		}
	}
	if cc.ClusterName != "" {
		args = append(args, kubeadmapi.Arg{Name: "cluster-name", Value: cc.ClusterName})
	}

	return append([]string{"kube-controller-manager"}, argumentsToCommand(args, cc.ControllerManager.ExtraArgs)...)
}

func buildControllerManagerPod(cfg *kubeadmapi.InitConfiguration) corev1.Pod {
	cc := &cfg.ClusterConfiguration
	mounts := controllerManagerVolumes(cc)

	container := corev1.Container{
		Name:            kubeadmconstants.KubeControllerManager,
		Image:           images.GetKubernetesImage(kubeadmconstants.KubeControllerManager, cc),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         controllerManagerCommand(cc),
		VolumeMounts:    mounts.mountsSlice(),
		LivenessProbe:   livenessProbe(controllerManagerProbeAddress(cc), "/healthz", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS),
		StartupProbe:    startupProbe(controllerManagerProbeAddress(cc), "/healthz", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS, controlPlaneHealthCheckTimeout(cfg)),
		Resources:       componentResources("200m"),
		Env:             mergeEnvVars(cc.ControllerManager.ExtraEnvs),
		Ports: []corev1.ContainerPort{
			{Name: kubeadmconstants.ProbePort, ContainerPort: kubeadmconstants.KubeControllerManagerPort, Protocol: corev1.ProtocolTCP},
		},
	}
	return componentPod(container, mounts.volumes, nil)
}

// --- kube-scheduler ---

func schedulerVolumes(cc *kubeadmapi.ClusterConfiguration) *hostPathMounts {
	m := newHostPathMounts()
	kubeconfig := filepath.Join(kubeadmconstants.KubernetesDir, kubeadmconstants.SchedulerKubeConfigFileName)
	m.add(kubeadmconstants.KubeConfigVolumeName, kubeconfig, kubeconfig, true, corev1.HostPathFileOrCreate)
	m.addExtra(cc.Scheduler.ExtraVolumes)
	return m
}

func schedulerCommand(cc *kubeadmapi.ClusterConfiguration) []string {
	kubeconfig := filepath.Join(kubeadmconstants.KubernetesDir, kubeadmconstants.SchedulerKubeConfigFileName)
	args := []kubeadmapi.Arg{
		{Name: "bind-address", Value: "127.0.0.1"},
		{Name: "leader-elect", Value: "true"},
		{Name: "kubeconfig", Value: kubeconfig},
		{Name: "authentication-kubeconfig", Value: kubeconfig},
		{Name: "authorization-kubeconfig", Value: kubeconfig},
	}
	return append([]string{"kube-scheduler"}, argumentsToCommand(args, cc.Scheduler.ExtraArgs)...)
}

func buildSchedulerPod(cfg *kubeadmapi.InitConfiguration) corev1.Pod {
	cc := &cfg.ClusterConfiguration
	mounts := schedulerVolumes(cc)

	container := corev1.Container{
		Name:            kubeadmconstants.KubeScheduler,
		Image:           images.GetKubernetesImage(kubeadmconstants.KubeScheduler, cc),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         schedulerCommand(cc),
		VolumeMounts:    mounts.mountsSlice(),
		LivenessProbe:   livenessProbe(schedulerProbeAddress(cc), "/livez", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS),
		ReadinessProbe:  readinessProbe(schedulerProbeAddress(cc), "/readyz", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS),
		StartupProbe:    startupProbe(schedulerProbeAddress(cc), "/livez", kubeadmconstants.ProbePort, corev1.URISchemeHTTPS, controlPlaneHealthCheckTimeout(cfg)),
		Resources:       componentResources("100m"),
		Env:             mergeEnvVars(cc.Scheduler.ExtraEnvs),
		Ports: []corev1.ContainerPort{
			{Name: kubeadmconstants.ProbePort, ContainerPort: kubeadmconstants.KubeSchedulerPort, Protocol: corev1.ProtocolTCP},
		},
	}
	return componentPod(container, mounts.volumes, nil)
}

// --- etcd ---

// etcdCommand mirrors kubeadm's getEtcdCommand for the init case only
// (initialCluster always empty): ControlPlaneManifests has no
// join/learners input, so "initial-cluster-state=existing" and the
// multi-member "initial-cluster" form never apply here.
func etcdCommand(cc *kubeadmapi.ClusterConfiguration, endpoint *kubeadmapi.APIEndpoint, nodeName string) []string {
	localhost := "127.0.0.1"
	if utilsnet.IsIPv6String(endpoint.AdvertiseAddress) {
		localhost = "::1"
	}
	clientURL := func(addr string) string {
		return "https://" + net.JoinHostPort(addr, strconv.Itoa(kubeadmconstants.EtcdListenClientPort))
	}
	advertiseClientURL := clientURL(endpoint.AdvertiseAddress)
	peerURL := "https://" + net.JoinHostPort(endpoint.AdvertiseAddress, strconv.Itoa(kubeadmconstants.EtcdListenPeerPort))

	args := []kubeadmapi.Arg{
		{Name: "name", Value: nodeName},
		{Name: "listen-client-urls", Value: clientURL(localhost) + "," + advertiseClientURL},
		{Name: "advertise-client-urls", Value: advertiseClientURL},
		{Name: "listen-peer-urls", Value: peerURL},
		{Name: "initial-advertise-peer-urls", Value: peerURL},
		{Name: "data-dir", Value: cc.Etcd.Local.DataDir},
		{Name: "cert-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.EtcdServerCertName)},
		{Name: "key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.EtcdServerKeyName)},
		{Name: "trusted-ca-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.EtcdCACertName)},
		{Name: "client-cert-auth", Value: "true"},
		{Name: "peer-cert-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.EtcdPeerCertName)},
		{Name: "peer-key-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.EtcdPeerKeyName)},
		{Name: "peer-trusted-ca-file", Value: filepath.Join(cc.CertificatesDir, kubeadmconstants.EtcdCACertName)},
		{Name: "peer-client-cert-auth", Value: "true"},
		{Name: "snapshot-count", Value: "10000"},
		{Name: "listen-metrics-urls", Value: "http://" + net.JoinHostPort(localhost, strconv.Itoa(kubeadmconstants.EtcdMetricsPort))},
		{Name: "initial-cluster", Value: nodeName + "=" + peerURL},
	}

	etcdImageTag := images.GetEtcdImageTag(cc, kubeadmconstants.SupportedEtcdVersion)
	if v, err := utilversion.ParseSemantic(etcdImageTag); err == nil && v.AtLeast(utilversion.MustParseSemantic("3.6.0")) {
		args = append(args,
			kubeadmapi.Arg{Name: "feature-gates", Value: "InitialCorruptCheck=true"},
			kubeadmapi.Arg{Name: "watch-progress-notify-interval", Value: "5s"},
		)
	} else {
		args = append(args,
			kubeadmapi.Arg{Name: "experimental-initial-corrupt-check", Value: "true"},
			kubeadmapi.Arg{Name: "experimental-watch-progress-notify-interval", Value: "5s"},
		)
	}

	return append([]string{"etcd"}, argumentsToCommand(args, cc.Etcd.Local.ExtraArgs)...)
}

func buildEtcdPod(cfg *kubeadmapi.InitConfiguration) corev1.Pod {
	cc := &cfg.ClusterConfiguration
	endpoint := &cfg.LocalAPIEndpoint
	nodeName := cfg.NodeRegistration.Name

	volumes := map[string]corev1.Volume{
		etcdDataVolumeName:  newVolume(etcdDataVolumeName, cc.Etcd.Local.DataDir, corev1.HostPathDirectoryOrCreate),
		etcdCertsVolumeName: newVolume(etcdCertsVolumeName, cc.CertificatesDir+"/etcd", corev1.HostPathDirectoryOrCreate),
	}
	probeHost, probePort, probeScheme := etcdProbeEndpoint(cc, utilsnet.IsIPv6String(endpoint.AdvertiseAddress))

	container := corev1.Container{
		Name:            kubeadmconstants.Etcd,
		Command:         etcdCommand(cc, endpoint, nodeName),
		Image:           images.GetEtcdImage(cc, kubeadmconstants.SupportedEtcdVersion),
		ImagePullPolicy: corev1.PullIfNotPresent,
		// Mounts are NOT alphabetically sorted here, unlike the other
		// three components: kubeadm's GetEtcdPodSpec builds this slice
		// literally in source order ([etcd-data, etcd-certs]), not
		// through VolumeMountMapToSlice.
		VolumeMounts: []corev1.VolumeMount{
			newVolumeMount(etcdDataVolumeName, cc.Etcd.Local.DataDir, false),
			newVolumeMount(etcdCertsVolumeName, cc.CertificatesDir+"/etcd", false),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
		},
		LivenessProbe:  livenessProbe(probeHost, "/livez", kubeadmconstants.ProbePort, probeScheme),
		ReadinessProbe: readinessProbe(probeHost, "/readyz", kubeadmconstants.ProbePort, probeScheme),
		StartupProbe:   startupProbe(probeHost, "/readyz", kubeadmconstants.ProbePort, probeScheme, controlPlaneHealthCheckTimeout(cfg)),
		Env:            mergeEnvVars(cc.Etcd.Local.ExtraEnvs),
		Ports: []corev1.ContainerPort{
			{Name: kubeadmconstants.ProbePort, ContainerPort: probePort, Protocol: corev1.ProtocolTCP},
		},
	}
	return componentPod(container, volumes, map[string]string{
		kubeadmconstants.EtcdAdvertiseClientUrlsAnnotationKey: "https://" + net.JoinHostPort(endpoint.AdvertiseAddress, strconv.Itoa(kubeadmconstants.EtcdListenClientPort)),
	})
}
