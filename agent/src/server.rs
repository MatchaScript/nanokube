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

/// Upper bound for a PushDesired message. Not transport tuning but a
/// guardrail: the blob carries /etc config only (realistic ceiling
/// 2-3MiB with signing + padding), so approaching this limit means
/// something that belongs in the OS image (/usr) leaked into the
/// confext. Widening this instead of fixing the leak is the wrong move.
/// (rev6 "bootstrap・transport・below-cluster")
pub const MAX_DESIRED_MESSAGE_BYTES: usize = 16 * 1024 * 1024;

/// desired.name arrives over the network and becomes a path component
/// (`/var/lib/confexts/<name>.raw`) and an extension-release filename;
/// reject anything that could traverse or hide (path separators, dot
/// prefixes, empty, oversized).
fn validate_name(name: &str) -> Result<(), Status> {
    let ok = !name.is_empty()
        && name.len() <= 255
        && !name.starts_with('.')
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'_' || b == b'-');
    if ok {
        Ok(())
    } else {
        let truncated: String = name.chars().take(64).collect();
        Err(Status::invalid_argument(format!(
            "invalid desired name {truncated:?} (len {}, showing first 64 chars): \
             must be non-empty, at most 255 chars, \
             not start with '.', and contain only [A-Za-z0-9._-]",
            name.len()
        )))
    }
}

/// Hands out a [`RealOps`] backed by `confexts_dir`/`bookkeeping_path`,
/// shelling out to the real `systemd-confext`/`bootc` binaries.
pub struct RealOpsProvider {
    confexts_dir: PathBuf,
    archive_dir: PathBuf,
    bookkeeping_path: PathBuf,
}

impl RealOpsProvider {
    pub fn new(
        confexts_dir: impl Into<PathBuf>,
        archive_dir: impl Into<PathBuf>,
        bookkeeping_path: impl Into<PathBuf>,
    ) -> Self {
        Self {
            confexts_dir: confexts_dir.into(),
            archive_dir: archive_dir.into(),
            bookkeeping_path: bookkeeping_path.into(),
        }
    }
}

impl OpsProvider for RealOpsProvider {
    fn ops(&self) -> Box<dyn Ops + Send> {
        Box::new(RealOps::new(
            self.confexts_dir.clone(),
            self.archive_dir.clone(),
            self.bookkeeping_path.clone(),
        ))
    }
}

/// The `Agent` gRPC service. Step 1 runs this dev-grade: plaintext, no
/// TLS (mTLS/PKI hardening is Step 5).
pub struct AgentService {
    ops_provider: Arc<dyn OpsProvider>,
    /// Serializes apply() across concurrent PushDesired calls: place /
    /// refresh on one node must not interleave.
    apply_lock: tokio::sync::Mutex<()>,
}

impl AgentService {
    pub fn new(ops_provider: impl OpsProvider + 'static) -> Self {
        Self {
            ops_provider: Arc::new(ops_provider),
            apply_lock: tokio::sync::Mutex::new(()),
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
        validate_name(&desired.name)?;
        let _guard = self.apply_lock.lock().await;
        let metadata = DesiredMetadata {
            name: desired.name,
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
        ApplyError::KubeletDownAfterRefresh => {
            Status::internal("kubelet was active before refresh and is not active after refresh")
        }
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;
    use std::time::Duration;

    use sha2::{Digest, Sha256};
    use tonic::Code;

    use super::*;
    use crate::desiredpb::agent_server::Agent as _;
    use crate::pipeline::{Bookkeeping, BootcStatus, OpsError};

    /// Records every `Ops` call (shared across the `Box<dyn Ops>` handed
    /// out per RPC and the test's assertions) with fresh/empty
    /// bookkeeping, matching
    /// `pipeline::tests::new_desired_places_refreshes_and_writes_bookkeeping`.
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

        fn archive_previous(
            &mut self,
            current_name: &str,
            previous_name: &str,
        ) -> Result<(), OpsError> {
            self.calls
                .lock()
                .unwrap()
                .push(format!("archive_previous:{current_name}:{previous_name}"));
            Ok(())
        }

        fn refresh(&mut self) -> Result<(), OpsError> {
            self.calls.lock().unwrap().push("refresh".to_string());
            Ok(())
        }

        fn kubelet_is_active(&mut self) -> Result<bool, OpsError> {
            self.calls
                .lock()
                .unwrap()
                .push("kubelet_is_active".to_string());
            Ok(false)
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

    /// Records only `place()`'s enter/exit markers around a 50ms sleep;
    /// every other method is a no-op success. Used to prove
    /// `push_desired` serializes concurrent calls: if apply() ran
    /// concurrently, two overlapping pushes would interleave their
    /// markers (enter, enter, exit, exit) instead of running back to
    /// back.
    #[derive(Clone, Default)]
    struct SlowOpsProvider {
        markers: Arc<Mutex<Vec<String>>>,
    }

    struct SlowOps {
        markers: Arc<Mutex<Vec<String>>>,
    }

    impl OpsProvider for SlowOpsProvider {
        fn ops(&self) -> Box<dyn Ops + Send> {
            Box::new(SlowOps {
                markers: Arc::clone(&self.markers),
            })
        }
    }

    impl Ops for SlowOps {
        fn place(&mut self, _name: &str, _blob: &[u8]) -> Result<(), OpsError> {
            self.markers.lock().unwrap().push("enter".to_string());
            std::thread::sleep(Duration::from_millis(50));
            self.markers.lock().unwrap().push("exit".to_string());
            Ok(())
        }

        fn archive_previous(
            &mut self,
            _current_name: &str,
            _previous_name: &str,
        ) -> Result<(), OpsError> {
            Ok(())
        }

        fn refresh(&mut self) -> Result<(), OpsError> {
            Ok(())
        }

        fn kubelet_is_active(&mut self) -> Result<bool, OpsError> {
            Ok(false)
        }

        fn read_bookkeeping(&mut self) -> Result<Bookkeeping, OpsError> {
            Ok(Bookkeeping::default())
        }

        fn write_bookkeeping(&mut self, _bk: &Bookkeeping) -> Result<(), OpsError> {
            Ok(())
        }

        fn bootc_status(&mut self) -> Result<Option<BootcStatus>, OpsError> {
            Ok(None)
        }

        fn bootc_switch(&mut self, _image_ref: &str) -> Result<(), OpsError> {
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
                "kubelet_is_active".to_string(),
                "place:v1-name".to_string(),
                "archive_previous:v1-name:".to_string(),
                "refresh".to_string(),
                "write_bookkeeping:v1-name".to_string(),
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

    #[tokio::test]
    async fn push_desired_rejects_path_traversal_names() {
        for bad in ["", "..", "../etc", "a/b", ".hidden", &"x".repeat(256)] {
            let provider = FakeOpsProvider::default();
            let calls = Arc::clone(&provider.calls);
            let service = AgentService::new(provider);
            let blob = b"blob".to_vec();
            let request = Request::new(Desired {
                name: bad.to_string(),
                blob_sha256: sha256_hex(&blob),
                blob,
            });
            let status = service.push_desired(request).await.unwrap_err();
            assert_eq!(status.code(), Code::InvalidArgument, "name {bad:?}");
            assert!(
                calls.lock().unwrap().is_empty(),
                "expected no Ops calls for name {bad:?}"
            );
        }
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn push_desired_calls_are_serialized() {
        // place() sleeps, so two overlapping pushes would interleave their
        // enter/exit markers if apply ran concurrently.
        let provider = SlowOpsProvider::default();
        let markers = Arc::clone(&provider.markers);
        let service = Arc::new(AgentService::new(provider));

        let mut handles = Vec::new();
        for i in 0..2 {
            let service = Arc::clone(&service);
            handles.push(tokio::spawn(async move {
                let blob = format!("blob-{i}").into_bytes();
                let request = Request::new(Desired {
                    name: format!("name-{i}"),
                    blob_sha256: sha256_hex(&blob),
                    blob,
                });
                service.push_desired(request).await.unwrap();
            }));
        }
        for h in handles {
            h.await.unwrap();
        }

        let markers = markers.lock().unwrap();
        assert_eq!(markers.len(), 4);
        // Strictly enter,exit,enter,exit — never enter,enter.
        assert_eq!(markers[0], "enter");
        assert_eq!(markers[1], "exit");
        assert_eq!(markers[2], "enter");
        assert_eq!(markers[3], "exit");
    }

    #[test]
    fn max_desired_message_bytes_is_16_mib() {
        assert_eq!(MAX_DESIRED_MESSAGE_BYTES, 16 * 1024 * 1024);
    }
}
