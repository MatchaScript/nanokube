package operator

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/MatchaScript/nanokube/contract/desiredpb"
)

// NewGRPCPush returns the real PushFunc: for each call it dials
// agentAddr fresh (plaintext -- Step 1 has no TLS; mTLS is Step 5),
// calls desiredpb.AgentClient.PushDesired with meta and rawPath's bytes,
// and closes the connection before returning. A per-call dial+close is
// fine for this Step 1 skeleton -- no connection pooling, matching how
// infrequently a reconcile actually needs to push.
//
// rawPath may not exist: Reconcile writes a sentinel BlobSha256 (see
// buildSkippedSentinel) and never creates rawPath when this host's DDI
// build tooling is missing. Pushing that case anyway would just have the
// agent's own checksum verification reject an empty blob against a
// sentinel that isn't a real sha256 of anything -- a real rejection, not
// a wrong one, but a confusing one to see over the wire when the actual
// cause (no build tool here) is already known locally. So NewGRPCPush
// skips the network call entirely in this specific case and logs why,
// returning nil: this mirrors NewLocalPush, which also never fails on a
// missing rawPath, and keeps "known local environment gap" from
// surfacing as a reconcile error that controller-runtime would retry
// forever. Any other read failure (e.g. a permissions error) is a real,
// unexpected problem and is returned as an error.
func NewGRPCPush(agentAddr string) PushFunc {
	return func(ctx context.Context, meta *desiredpb.DesiredMetadata, rawPath string) error {
		logger := log.FromContext(ctx)

		blob, err := os.ReadFile(rawPath)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Info("skipping push: no DDI blob was built for this document (build tooling unavailable on this host), nothing to push",
					"name", meta.Name, "rawPath", rawPath)
				return nil
			}
			return fmt.Errorf("operator: read blob %s: %w", rawPath, err)
		}

		conn, err := grpc.NewClient(agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("operator: dial agent %s: %w", agentAddr, err)
		}
		defer conn.Close()

		client := desiredpb.NewAgentClient(conn)
		resp, err := client.PushDesired(ctx, &desiredpb.Desired{
			Name:       meta.Name,
			BlobSha256: meta.BlobSha256,
			Blob:       blob,
		})
		if err != nil {
			return fmt.Errorf("operator: PushDesired to %s: %w", agentAddr, err)
		}

		logger.Info("pushed to agent", "agentAddr", agentAddr, "desiredName", resp.GetDesiredName())
		return nil
	}
}
