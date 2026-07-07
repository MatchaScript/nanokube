//! gRPC entry point: implements `desiredpb::agent_server::Agent` on top
//! of the same [`apply`] pipeline the `apply --once` CLI uses.
//!
//! [`AgentService`] doesn't hold an `Ops` directly — it holds an
//! [`OpsProvider`] and asks it for a fresh `Ops` on every `PushDesired`
//! call. That's the seam tests use to inject a fake without shelling out
//! to `systemd-confext`/`bootc`; production wires up [`RealOpsProvider`],
//! which hands out a fresh [`RealOps`] per call.

use std::path::PathBuf;
use std::sync::Arc;

use tonic::{Request, Response, Status};

use crate::desiredpb::{Desired, PushDesiredResponse, agent_server};
use crate::ops::RealOps;
use crate::pipeline::{ApplyError, DesiredMetadata, Ops, apply};

/// Produces a fresh [`Ops`] for each `PushDesired` call. Production code
/// uses [`RealOpsProvider`]; tests substitute a fake that hands out an
/// in-memory `Ops` recording every call it receives.
pub trait OpsProvider: Send + Sync {
    fn ops(&self) -> Box<dyn Ops + Send>;
}

/// Hands out a [`RealOps`] backed by `confexts_dir`/`bookkeeping_path`,
/// shelling out to the real `systemd-confext`/`bootc` binaries.
pub struct RealOpsProvider {
    confexts_dir: PathBuf,
    bookkeeping_path: PathBuf,
}

impl RealOpsProvider {
    pub fn new(confexts_dir: impl Into<PathBuf>, bookkeeping_path: impl Into<PathBuf>) -> Self {
        Self {
            confexts_dir: confexts_dir.into(),
            bookkeeping_path: bookkeeping_path.into(),
        }
    }
}

impl OpsProvider for RealOpsProvider {
    fn ops(&self) -> Box<dyn Ops + Send> {
        Box::new(RealOps::new(
            self.confexts_dir.clone(),
            self.bookkeeping_path.clone(),
        ))
    }
}

/// The `Agent` gRPC service. Step 1 runs this dev-grade: plaintext, no
/// TLS (mTLS/PKI hardening is Step 5).
pub struct AgentService {
    ops_provider: Arc<dyn OpsProvider>,
}

impl AgentService {
    pub fn new(ops_provider: impl OpsProvider + 'static) -> Self {
        Self {
            ops_provider: Arc::new(ops_provider),
        }
    }
}

#[tonic::async_trait]
impl agent_server::Agent for AgentService {
    async fn push_desired(
        &self,
        request: Request<Desired>,
    ) -> Result<Response<PushDesiredResponse>, Status> {
        let desired = request.into_inner();
        let metadata = DesiredMetadata {
            name: desired.name,
            target_image_digest: desired.target_image_digest,
            blob_sha256: desired.blob_sha256,
        };

        let mut ops = self.ops_provider.ops();
        apply(&metadata, &desired.blob, ops.as_mut()).map_err(to_status)?;

        Ok(Response::new(PushDesiredResponse {
            desired_name: metadata.name,
        }))
    }
}

/// Checksum mismatch is a client-supplied bad request
/// (`InvalidArgument`); anything from `Ops` is this node's own
/// filesystem/subprocess failing (`Internal`).
fn to_status(err: ApplyError) -> Status {
    match err {
        ApplyError::ChecksumMismatch { want, got } => {
            Status::invalid_argument(format!("blob checksum mismatch: want {want}, got {got}"))
        }
        ApplyError::Ops(e) => Status::internal(e.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use sha2::{Digest, Sha256};
    use tonic::Code;

    use super::*;
    use crate::desiredpb::agent_server::Agent as _;
    use crate::pipeline::{Bookkeeping, BootcStatus, OpsError};

    /// Records every `Ops` call (shared across the `Box<dyn Ops>` handed
    /// out per RPC and the test's assertions) with fresh/empty
    /// bookkeeping and a non-bootc host, matching
    /// `pipeline::tests::new_desired_places_refreshes_and_writes_bookkeeping_before_bootc`.
    #[derive(Clone, Default)]
    struct FakeOpsProvider {
        calls: Arc<Mutex<Vec<String>>>,
    }

    struct FakeOps {
        calls: Arc<Mutex<Vec<String>>>,
    }

    impl OpsProvider for FakeOpsProvider {
        fn ops(&self) -> Box<dyn Ops + Send> {
            Box::new(FakeOps {
                calls: Arc::clone(&self.calls),
            })
        }
    }

    impl Ops for FakeOps {
        fn place(&mut self, name: &str, _blob: &[u8]) -> Result<(), OpsError> {
            self.calls.lock().unwrap().push(format!("place:{name}"));
            Ok(())
        }

        fn refresh(&mut self) -> Result<(), OpsError> {
            self.calls.lock().unwrap().push("refresh".to_string());
            Ok(())
        }

        fn read_bookkeeping(&mut self) -> Result<Bookkeeping, OpsError> {
            self.calls
                .lock()
                .unwrap()
                .push("read_bookkeeping".to_string());
            Ok(Bookkeeping::default())
        }

        fn write_bookkeeping(&mut self, bk: &Bookkeeping) -> Result<(), OpsError> {
            self.calls
                .lock()
                .unwrap()
                .push(format!("write_bookkeeping:{}", bk.desired_name));
            Ok(())
        }

        fn bootc_status(&mut self) -> Result<Option<BootcStatus>, OpsError> {
            self.calls.lock().unwrap().push("bootc_status".to_string());
            Ok(None)
        }

        fn bootc_switch(&mut self, image_ref: &str) -> Result<(), OpsError> {
            self.calls
                .lock()
                .unwrap()
                .push(format!("bootc_switch:{image_ref}"));
            Ok(())
        }
    }

    fn sha256_hex(data: &[u8]) -> String {
        Sha256::digest(data)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect()
    }

    #[tokio::test]
    async fn push_desired_success_goes_through_the_apply_pipeline() {
        let blob = b"confext-blob".to_vec();
        let blob_sha256 = sha256_hex(&blob);
        let provider = FakeOpsProvider::default();
        let calls = Arc::clone(&provider.calls);
        let service = AgentService::new(provider);

        let request = Request::new(Desired {
            name: "v1-name".to_string(),
            target_image_digest: "sha256:TARGET".to_string(),
            blob_sha256,
            blob,
        });

        let response = service.push_desired(request).await.unwrap();
        assert_eq!(response.into_inner().desired_name, "v1-name");

        // The pipeline's own steps actually ran, in order — this is the
        // proof the RPC went through apply() and not just a stub.
        assert_eq!(
            *calls.lock().unwrap(),
            vec![
                "read_bookkeeping".to_string(),
                "place:v1-name".to_string(),
                "refresh".to_string(),
                "write_bookkeeping:v1-name".to_string(),
                "bootc_status".to_string(),
            ]
        );
    }

    #[tokio::test]
    async fn push_desired_checksum_mismatch_is_invalid_argument_and_touches_nothing() {
        let provider = FakeOpsProvider::default();
        let calls = Arc::clone(&provider.calls);
        let service = AgentService::new(provider);

        let request = Request::new(Desired {
            name: "v1-name".to_string(),
            target_image_digest: "sha256:TARGET".to_string(),
            blob_sha256: "0".repeat(64), // deliberately wrong
            blob: b"confext-blob".to_vec(),
        });

        let status = service.push_desired(request).await.unwrap_err();

        assert_eq!(status.code(), Code::InvalidArgument);
        assert!(status.message().contains("checksum"));
        assert!(
            calls.lock().unwrap().is_empty(),
            "expected no Ops calls, got {:?}",
            calls.lock().unwrap()
        );
    }
}
