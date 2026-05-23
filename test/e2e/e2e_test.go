//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestNanokubeE2E is the suite entrypoint. All Test<NN>_* methods on
// NanokubeE2ESuite are dispatched by testify in lexicographic order.
func TestNanokubeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e suite skipped in -short")
	}
	suite.Run(t, new(NanokubeE2ESuite))
}
