//! Core apply pipeline.
//!
//! `apply` takes a fully-rendered desired document (metadata + confext DDI
//! blob, already built server-side by the operator) and reconciles the
//! node to it: verify the blob's checksum, place + refresh the confext if
//! it changed, and record the applied name as a fact. There is no render
//! step here and no cache of the last desired document across calls —
//! every call is handed a fresh `DesiredMetadata` + blob and starts from
//! what `Ops` reports on disk.
//!
//! `Ops` is the injectable seam for everything that touches the outside
//! world (filesystem, systemd-confext, bootc). This module performs no
//! real I/O itself; a later task provides the real implementation.

use std::fmt;

use sha2::{Digest, Sha256};

/// Mirrors `contract.desired.v1.DesiredMetadata`: a `Desired` without the
/// blob payload itself.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DesiredMetadata {
    pub name: String,
    pub blob_sha256: String,
}

/// A reduction of `bootc status --json --format-version=1` to the fields
/// the pipeline needs. `staged_*` is `None` when there is no staged
/// deployment.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BootcStatus {
    pub booted_image: String,
    pub booted_digest: String,
    pub staged_image: Option<String>,
    pub staged_digest: Option<String>,
}

/// On-disk record of what the agent last applied. A fact, written only
/// after a successful place + refresh (never a prediction of what a
/// future boot should look like — rev6 abolished the expected-digest
/// slot). JSON key `desiredName` matches the abandoned Go agent's
/// bookkeeping so ops.rs's serde impl stays a straight port.
/// An empty string means "unset".
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Bookkeeping {
    pub desired_name: String,
}

/// Opaque error from an `Ops` call.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OpsError(pub String);

impl fmt::Display for OpsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.0)
    }
}

impl std::error::Error for OpsError {}

/// The injectable seam between the pipeline and the outside world.
/// Production code implements this against real files / systemd-confext /
/// bootc; tests implement it against an in-memory fake.
pub trait Ops {
    /// Place the confext DDI blob under the given desired-document name.
    fn place(&mut self, name: &str, blob: &[u8]) -> Result<(), OpsError>;
    /// Refresh confexts so the newly placed blob takes effect.
    fn refresh(&mut self) -> Result<(), OpsError>;
    fn read_bookkeeping(&mut self) -> Result<Bookkeeping, OpsError>;
    fn write_bookkeeping(&mut self, bk: &Bookkeeping) -> Result<(), OpsError>;
    /// `Ok(None)` means "not a bootc host": config delivery still
    /// succeeds, image staging is simply skipped.
    fn bootc_status(&mut self) -> Result<Option<BootcStatus>, OpsError>;
    fn bootc_switch(&mut self, image_ref: &str) -> Result<(), OpsError>;
}

/// Why `apply` failed.
#[derive(Debug)]
pub enum ApplyError {
    /// `blob`'s sha256 didn't match `DesiredMetadata::blob_sha256`.
    ChecksumMismatch {
        want: String,
        got: String,
    },
    Ops(OpsError),
}

impl fmt::Display for ApplyError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ApplyError::ChecksumMismatch { want, got } => {
                write!(f, "blob checksum mismatch: want {want}, got {got}")
            }
            ApplyError::Ops(e) => write!(f, "{e}"),
        }
    }
}

impl std::error::Error for ApplyError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            ApplyError::Ops(e) => Some(e),
            ApplyError::ChecksumMismatch { .. } => None,
        }
    }
}

impl From<OpsError> for ApplyError {
    fn from(e: OpsError) -> Self {
        ApplyError::Ops(e)
    }
}

/// Verifies `blob` against `desired.blob_sha256`, places + refreshes it
/// if `desired.name` differs from what's already applied (a no-op skip
/// otherwise) and records the applied name.
///
/// On checksum mismatch, returns immediately: nothing on `ops` is called.
pub fn apply(desired: &DesiredMetadata, blob: &[u8], ops: &mut dyn Ops) -> Result<(), ApplyError> {
    verify_checksum(desired, blob)?;

    let bk = ops.read_bookkeeping()?;
    if bk.desired_name != desired.name {
        ops.place(&desired.name, blob)?;
        ops.refresh()?;
        ops.write_bookkeeping(&Bookkeeping {
            desired_name: desired.name.clone(),
        })?;
    }

    Ok(())
}

fn verify_checksum(desired: &DesiredMetadata, blob: &[u8]) -> Result<(), ApplyError> {
    let got = hex_encode(Sha256::digest(blob).as_slice());
    if got != desired.blob_sha256 {
        return Err(ApplyError::ChecksumMismatch {
            want: desired.blob_sha256.clone(),
            got,
        });
    }
    Ok(())
}

fn hex_encode(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    struct FakeOps {
        calls: Vec<String>,
        bookkeeping: Bookkeeping,
        bootc_status_response: Result<Option<BootcStatus>, OpsError>,
        write_bookkeeping_calls: Vec<Bookkeeping>,
        switch_targets: Vec<String>,
    }

    impl Default for FakeOps {
        fn default() -> Self {
            FakeOps {
                calls: Vec::new(),
                bookkeeping: Bookkeeping::default(),
                bootc_status_response: Ok(None),
                write_bookkeeping_calls: Vec::new(),
                switch_targets: Vec::new(),
            }
        }
    }

    impl Ops for FakeOps {
        fn place(&mut self, name: &str, _blob: &[u8]) -> Result<(), OpsError> {
            self.calls.push(format!("place:{name}"));
            Ok(())
        }

        fn refresh(&mut self) -> Result<(), OpsError> {
            self.calls.push("refresh".to_string());
            Ok(())
        }

        fn read_bookkeeping(&mut self) -> Result<Bookkeeping, OpsError> {
            self.calls.push("read_bookkeeping".to_string());
            Ok(self.bookkeeping.clone())
        }

        fn write_bookkeeping(&mut self, bk: &Bookkeeping) -> Result<(), OpsError> {
            self.calls
                .push(format!("write_bookkeeping:{}", bk.desired_name));
            self.bookkeeping = bk.clone();
            self.write_bookkeeping_calls.push(bk.clone());
            Ok(())
        }

        fn bootc_status(&mut self) -> Result<Option<BootcStatus>, OpsError> {
            self.calls.push("bootc_status".to_string());
            self.bootc_status_response.clone()
        }

        fn bootc_switch(&mut self, image_ref: &str) -> Result<(), OpsError> {
            self.calls.push(format!("bootc_switch:{image_ref}"));
            self.switch_targets.push(image_ref.to_string());
            Ok(())
        }
    }

    fn sha256_hex(data: &[u8]) -> String {
        hex_encode(Sha256::digest(data).as_slice())
    }

    /// A `DesiredMetadata` whose `blob_sha256` genuinely matches `blob`.
    fn desired(name: &str, blob: &[u8]) -> DesiredMetadata {
        DesiredMetadata {
            name: name.to_string(),
            blob_sha256: sha256_hex(blob),
        }
    }

    #[test]
    fn checksum_mismatch_short_circuits_before_any_ops_call() {
        let blob = b"real-blob";
        let mut d = desired("v1", blob);
        d.blob_sha256 = "0".repeat(64); // deliberately wrong
        let mut ops = FakeOps::default();

        let err = apply(&d, blob, &mut ops).unwrap_err();

        match err {
            ApplyError::ChecksumMismatch { want, got } => {
                assert_eq!(want, d.blob_sha256);
                assert_eq!(got, sha256_hex(blob));
            }
            other => panic!("expected ChecksumMismatch, got {other:?}"),
        }
        assert!(
            ops.calls.is_empty(),
            "expected no Ops calls, got {:?}",
            ops.calls
        );
    }

    #[test]
    fn new_desired_places_refreshes_and_writes_bookkeeping() {
        let blob = b"confext-blob";
        let d = desired("v1-name", blob);
        let mut ops = FakeOps::default(); // empty bookkeeping

        apply(&d, blob, &mut ops).unwrap();

        assert_eq!(
            ops.calls,
            vec![
                "read_bookkeeping".to_string(),
                "place:v1-name".to_string(),
                "refresh".to_string(),
                "write_bookkeeping:v1-name".to_string(),
            ]
        );
        assert_eq!(
            ops.write_bookkeeping_calls,
            vec![Bookkeeping {
                desired_name: "v1-name".to_string(),
            }]
        );
    }
}
