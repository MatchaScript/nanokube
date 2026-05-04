package healthcheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
)

// fakeServer is the shared httptest fixture used by apiserver_test.go
// and cluster_test.go. /readyz returns 503 until ready flips; node and
// pod GETs read from in-memory maps populated via setNodeReady /
// setPodReady.
type fakeServer struct {
	t      *testing.T
	ready  atomic.Bool
	mu     sync.Mutex
	nodes  map[string]*corev1.Node
	pods   map[string]*corev1.Pod
	server *httptest.Server
	client kubernetes.Interface
}

func newFakeAPIServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		t:     t,
		nodes: map[string]*corev1.Node{},
		pods:  map[string]*corev1.Pod{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)

	cs, err := kubernetes.NewForConfig(&restclient.Config{Host: f.server.URL})
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	f.client = cs
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/readyz":
		if f.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return

	case r.Method == http.MethodGet && matchNode(r.URL.Path) != "":
		name := matchNode(r.URL.Path)
		f.mu.Lock()
		n, ok := f.nodes[name]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, n)
		return

	case r.Method == http.MethodGet && matchPod(r.URL.Path) != "":
		name := matchPod(r.URL.Path)
		f.mu.Lock()
		p, ok := f.pods[name]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, p)
		return
	}

	http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
}

func matchNode(path string) string {
	const prefix = "/api/v1/nodes/"
	if len(path) > len(prefix) && path[:len(prefix)] == prefix {
		return path[len(prefix):]
	}
	return ""
}

func matchPod(path string) string {
	const prefix = "/api/v1/namespaces/kube-system/pods/"
	if len(path) > len(prefix) && path[:len(prefix)] == prefix {
		return path[len(prefix):]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (f *fakeServer) setNodeReady(name string, ready bool) {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[name] = &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: status},
			},
		},
	}
}

func (f *fakeServer) setPodReady(name string, ready bool) {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pods[name] = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: status},
			},
		},
	}
}
