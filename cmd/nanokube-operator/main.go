// Command nanokube-operator is the Step 1 skeleton nanokube-operator: a
// controller-runtime manager that reconciles a single, fixed ConfigMap
// (the Step 1 stand-in desired source; CRDs and the northbound API land
// in Step 4) into a rendered + built confext DDI, then pushes the result
// to nanokube-agent over real gRPC (--push-mode=grpc, the default) or,
// for local dev without a running agent, writes it to a local directory
// instead (--push-mode=local). See internal/operator for the reconcile
// logic and docs/nanokube/2026-07-06-step1-implementation-plan-rev5.md,
// 実装項目5.
package main

import (
	"flag"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/MatchaScript/nanokube/internal/operator"
)

func main() {
	var (
		configMapName      string
		configMapNamespace string
		outputDir          string
		agentAddr          string
		pushMode           string
	)
	flag.StringVar(&configMapName, "configmap-name", "nanokube-desired-input",
		"name of the ConfigMap this operator reconciles (Step 1 stand-in desired source; CRDs land in Step 4)")
	flag.StringVar(&configMapNamespace, "configmap-namespace", "default",
		"namespace of the ConfigMap this operator reconciles")
	flag.StringVar(&outputDir, "output-dir", "/var/lib/nanokube-operator/pushed",
		"local directory the push stand-in writes <name>.raw/<name>.json into (--push-mode=local), or where the built <name>.raw is read from before a real push (--push-mode=grpc)")
	flag.StringVar(&agentAddr, "agent-addr", "127.0.0.1:9090",
		"address of nanokube-agent's gRPC endpoint")
	flag.StringVar(&pushMode, "push-mode", "grpc",
		"how to deliver the desired document: \"grpc\" dials --agent-addr and calls the real nanokube-agent gRPC service (production default); \"local\" writes it to --output-dir instead, for local dev without a running agent")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	setupLog := ctrl.Log.WithName("setup")

	var push operator.PushFunc
	switch pushMode {
	case "grpc":
		push = operator.NewGRPCPush(agentAddr)
	case "local":
		push = operator.NewLocalPush(outputDir, agentAddr)
	default:
		setupLog.Error(fmt.Errorf("unknown --push-mode %q", pushMode), `must be "grpc" or "local"`)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// NANOKUBE_FILE_CONTEXTS names an SELinux file_contexts database
	// (e.g. /etc/selinux/targeted/contexts/files/file_contexts) to bake
	// build-time labels into every built DDI (IMPLEMENTATION_PLAN.md §6).
	// Unset means no labeling, matching non-SELinux dev hosts.
	fileContextsPath := os.Getenv("NANOKUBE_FILE_CONTEXTS")

	r := &operator.Reconciler{
		Client:             mgr.GetClient(),
		ConfigMapName:      configMapName,
		ConfigMapNamespace: configMapNamespace,
		OutputDir:          outputDir,
		FileContextsPath:   fileContextsPath,
		Push:               push,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller")
		os.Exit(1)
	}

	setupLog.Info("starting nanokube-operator",
		"configmapName", configMapName,
		"configmapNamespace", configMapNamespace,
		"outputDir", outputDir,
		"agentAddr", agentAddr,
		"pushMode", pushMode,
		"fileContextsPath", fileContextsPath,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
