package kubeclient

import "testing"

func TestLoadAdmin_InvalidPathFails(t *testing.T) {
	_, err := LoadAdmin("/nonexistent/admin.conf")
	if err == nil {
		t.Fatal("LoadAdmin on missing file = nil")
	}
}
