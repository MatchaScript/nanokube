package healthcheck

import (
	"context"
	"testing"
	"time"
)

func TestWaitForControlPlane_AllReadyReturns(t *testing.T) {
	f := newFakeAPIServer(t)
	const node = "cp-1"
	f.setNodeReady(node, true)
	f.setPodReady("kube-apiserver-"+node, true)
	f.setPodReady("kube-controller-manager-"+node, true)
	f.setPodReady("kube-scheduler-"+node, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForControlPlane(ctx, f.client, node); err != nil {
		t.Fatalf("WaitForControlPlane: %v", err)
	}
}

// If even one static pod never reaches Ready the call must time out.
// Mirrors the production case where kube-controller-manager crash-loops
// while /readyz on the apiserver itself stays green.
func TestWaitForControlPlane_MissingOnePodTimesOut(t *testing.T) {
	f := newFakeAPIServer(t)
	const node = "cp-1"
	f.setNodeReady(node, true)
	f.setPodReady("kube-apiserver-"+node, true)
	f.setPodReady("kube-controller-manager-"+node, false) // stuck not-ready
	f.setPodReady("kube-scheduler-"+node, true)

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	err := WaitForControlPlane(ctx, f.client, node)
	if err == nil {
		t.Fatal("WaitForControlPlane with not-ready CM = nil; want timeout")
	}
}

// Node reports NodeReady=False (kubelet running but CNI not up). Every
// other pod is Ready but the overall check must still block.
func TestWaitForControlPlane_NodeNotReadyTimesOut(t *testing.T) {
	f := newFakeAPIServer(t)
	const node = "cp-1"
	f.setNodeReady(node, false)
	f.setPodReady("kube-apiserver-"+node, true)
	f.setPodReady("kube-controller-manager-"+node, true)
	f.setPodReady("kube-scheduler-"+node, true)

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	if err := WaitForControlPlane(ctx, f.client, node); err == nil {
		t.Fatal("WaitForControlPlane with NodeReady=False = nil; want timeout")
	}
}

// Pod becomes Ready partway through — must be picked up without the
// caller needing to re-invoke.
func TestWaitForControlPlane_EventuallyReadyPasses(t *testing.T) {
	f := newFakeAPIServer(t)
	const node = "cp-1"
	f.setNodeReady(node, true)
	f.setPodReady("kube-apiserver-"+node, true)
	f.setPodReady("kube-controller-manager-"+node, false)
	f.setPodReady("kube-scheduler-"+node, true)

	go func() {
		time.Sleep(300 * time.Millisecond)
		f.setPodReady("kube-controller-manager-"+node, true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForControlPlane(ctx, f.client, node); err != nil {
		t.Fatalf("WaitForControlPlane: %v", err)
	}
}
