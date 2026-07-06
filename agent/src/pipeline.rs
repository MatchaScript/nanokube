//! Core apply pipeline.
//!
//! `apply` takes a fully-rendered desired document (metadata + confext DDI
//! blob, already built server-side by the operator) and reconciles the
//! node to it: verify the blob's checksum, place + refresh the confext if
//! it changed, bookkeep what was applied, then reconcile the booted OS
//! image via bootc. There is no render step here and no cache of the last
//! desired document across calls — every call is handed a fresh
//! `DesiredMetadata` + blob and starts from what `Ops` reports on disk.
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
    pub target_image_digest: String,
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

/// On-disk record of what the agent last applied. Field names/semantics
/// match the abandoned Go agent's bookkeeping (JSON keys `desiredName` /
/// `expectedDigest`), so a later task's serde impl is a straight port.
/// An empty string means "unset".
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Bookkeeping {
    pub expected_digest: String,
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

/// Verifies `blob` against `desired.blob_sha256`, places + refreshes +
/// bookkeeps it if `desired.name` differs from what's already applied
/// (a no-op skip otherwise), then reconciles the booted OS image via
/// bootc (skipped entirely on a non-bootc host).
///
/// On checksum mismatch, returns immediately: nothing on `ops` is called.
pub fn apply(desired: &DesiredMetadata, blob: &[u8], ops: &mut dyn Ops) -> Result<(), ApplyError> {
    verify_checksum(desired, blob)?;

    let mut bk = ops.read_bookkeeping()?;
    if bk.desired_name != desired.name {
        ops.place(&desired.name, blob)?;
        ops.refresh()?;
        bk.desired_name = desired.name.clone();
        bk.expected_digest = desired.target_image_digest.clone();
        ops.write_bookkeeping(&bk)?;
    }

    let Some(status) = ops.bootc_status()? else {
        // Not a bootc host: config delivery is already done above.
        return Ok(());
    };

    let target = desired.target_image_digest.as_str();
    let has_staged = status.staged_digest.is_some();
    let already_at_target = target == status.booted_digest
        || (has_staged && status.staged_digest.as_deref() == Some(target));

    if !already_at_target {
        let image_ref = format!("{}@{}", repo_without_ref(&status.booted_image), target);
        ops.bootc_switch(&image_ref)?;
    }

    if !has_staged && !bk.expected_digest.is_empty() {
        bk.expected_digest.clear();
        ops.write_bookkeeping(&bk)?;
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

/// Strips the tag/digest suffix off an image reference, leaving the bare
/// repo (registry host + path). Used to derive a resolvable `bootc
/// switch` target from the currently booted image's pull-spec (its repo
/// persists across updates) plus the new desired digest: `bootc switch`
/// cannot resolve a bare digest on its own.
fn repo_without_ref(image: &str) -> &str {
    if let Some(at) = image.rfind('@') {
        return &image[..at];
    }
    match image.rfind('/') {
        // A ':' after the last '/' is a tag separator. A ':' before it
        // (e.g. "localhost:5000/...") is a registry port, not a tag.
        Some(slash) => match image[slash..].rfind(':') {
            Some(colon) => &image[..slash + colon],
            None => image,
        },
        None => match image.rfind(':') {
            Some(colon) => &image[..colon],
            None => image,
        },
    }
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
            self.calls.push(format!(
                "write_bookkeeping:{}:{}",
                bk.desired_name, bk.expected_digest
            ));
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
    fn desired(name: &str, target_digest: &str, blob: &[u8]) -> DesiredMetadata {
        DesiredMetadata {
            name: name.to_string(),
            target_image_digest: target_digest.to_string(),
            blob_sha256: sha256_hex(blob),
        }
    }

    #[test]
    fn checksum_mismatch_short_circuits_before_any_ops_call() {
        let blob = b"real-blob";
        let mut d = desired("v1", "sha256:AAA", blob);
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
    fn new_desired_places_refreshes_and_writes_bookkeeping_before_bootc() {
        let blob = b"confext-blob";
        let d = desired("v1-name", "sha256:TARGET", blob);
        let mut ops = FakeOps::default(); // empty bookkeeping, Ok(None) bootc status

        apply(&d, blob, &mut ops).unwrap();

        assert_eq!(
            ops.calls,
            vec![
                "read_bookkeeping".to_string(),
                "place:v1-name".to_string(),
                "refresh".to_string(),
                "write_bookkeeping:v1-name:sha256:TARGET".to_string(),
                "bootc_status".to_string(),
            ]
        );
        assert_eq!(
            ops.write_bookkeeping_calls,
            vec![Bookkeeping {
                desired_name: "v1-name".to_string(),
                expected_digest: "sha256:TARGET".to_string(),
            }]
        );
    }

    #[test]
    fn idempotent_config_skip_still_runs_bootc_switch() {
        let blob = b"same-blob";
        let d = desired("already-applied", "sha256:NEW", blob);
        let mut ops = FakeOps {
            bookkeeping: Bookkeeping {
                desired_name: "already-applied".to_string(),
                expected_digest: String::new(),
            },
            bootc_status_response: Ok(Some(BootcStatus {
                booted_image: "quay.io/foo/node:v1".to_string(),
                booted_digest: "sha256:OLD".to_string(),
                staged_image: None,
                staged_digest: None,
            })),
            ..FakeOps::default()
        };

        apply(&d, blob, &mut ops).unwrap();

        // No place/refresh/write_bookkeeping: config was already applied.
        // bootc status/switch still ran.
        assert_eq!(
            ops.calls,
            vec![
                "read_bookkeeping".to_string(),
                "bootc_status".to_string(),
                "bootc_switch:quay.io/foo/node@sha256:NEW".to_string(),
            ]
        );
    }

    #[test]
    fn bug1_regression_switch_uses_full_image_ref_not_bare_digest() {
        let blob = b"blob";
        let d = desired("name", "sha256:BBB", blob);
        let mut ops = FakeOps {
            bookkeeping: Bookkeeping {
                desired_name: "name".to_string(),
                expected_digest: String::new(),
            },
            bootc_status_response: Ok(Some(BootcStatus {
                booted_image: "quay.io/foo/node:v1".to_string(),
                booted_digest: "sha256:AAA".to_string(),
                staged_image: None,
                staged_digest: None,
            })),
            ..FakeOps::default()
        };

        apply(&d, blob, &mut ops).unwrap();

        // Regression guard for bug 1: must be a resolvable full
        // reference, not the bare "sha256:BBB" digest.
        assert_eq!(
            ops.switch_targets,
            vec!["quay.io/foo/node@sha256:BBB".to_string()]
        );
    }

    #[test]
    fn already_booted_at_target_skips_switch() {
        let blob = b"blob";
        let d = desired("name", "sha256:AAA", blob);
        let mut ops = FakeOps {
            bookkeeping: Bookkeeping {
                desired_name: "name".to_string(),
                expected_digest: String::new(),
            },
            bootc_status_response: Ok(Some(BootcStatus {
                booted_image: "quay.io/foo/node@sha256:AAA".to_string(),
                booted_digest: "sha256:AAA".to_string(),
                staged_image: None,
                staged_digest: None,
            })),
            ..FakeOps::default()
        };

        apply(&d, blob, &mut ops).unwrap();

        assert!(ops.switch_targets.is_empty());
    }

    #[test]
    fn already_staged_at_target_skips_switch() {
        let blob = b"blob";
        let d = desired("name", "sha256:BBB", blob);
        let mut ops = FakeOps {
            bookkeeping: Bookkeeping {
                desired_name: "name".to_string(),
                expected_digest: "sha256:BBB".to_string(),
            },
            bootc_status_response: Ok(Some(BootcStatus {
                booted_image: "quay.io/foo/node:v1".to_string(),
                booted_digest: "sha256:AAA".to_string(),
                staged_image: Some("quay.io/foo/node@sha256:BBB".to_string()),
                staged_digest: Some("sha256:BBB".to_string()),
            })),
            ..FakeOps::default()
        };

        apply(&d, blob, &mut ops).unwrap();

        assert!(ops.switch_targets.is_empty());
        // Staged deployment present, so no bookkeeping clear either.
        assert!(ops.write_bookkeeping_calls.is_empty());
    }

    #[test]
    fn not_a_bootc_host_skips_staging_but_config_delivery_still_happens() {
        let blob = b"blob";
        let d = desired("fresh-name", "sha256:TARGET", blob);
        let mut ops = FakeOps::default(); // fresh bookkeeping, Ok(None) bootc status

        apply(&d, blob, &mut ops).unwrap();

        assert!(ops.calls.contains(&"place:fresh-name".to_string()));
        assert!(ops.calls.contains(&"refresh".to_string()));
        assert!(ops.switch_targets.is_empty());
        assert_eq!(ops.calls.last(), Some(&"bootc_status".to_string()));
    }

    #[test]
    fn bookkeeping_clear_when_nothing_staged() {
        let blob = b"blob";
        let d = desired("name", "sha256:AAA", blob);
        let mut ops = FakeOps {
            bookkeeping: Bookkeeping {
                desired_name: "name".to_string(),
                expected_digest: "sha256:STALE".to_string(),
            },
            bootc_status_response: Ok(Some(BootcStatus {
                booted_image: "quay.io/foo/node@sha256:AAA".to_string(),
                booted_digest: "sha256:AAA".to_string(),
                staged_image: None,
                staged_digest: None,
            })),
            ..FakeOps::default()
        };

        apply(&d, blob, &mut ops).unwrap();

        assert_eq!(
            ops.write_bookkeeping_calls,
            vec![Bookkeeping {
                desired_name: "name".to_string(),
                expected_digest: String::new(),
            }]
        );
    }

    #[test]
    fn repo_without_ref_strips_tag() {
        assert_eq!(repo_without_ref("quay.io/foo/node:v1"), "quay.io/foo/node");
    }

    #[test]
    fn repo_without_ref_strips_digest() {
        assert_eq!(
            repo_without_ref("quay.io/foo/node@sha256:AAA"),
            "quay.io/foo/node"
        );
    }

    #[test]
    fn repo_without_ref_port_not_mistaken_for_tag() {
        assert_eq!(
            repo_without_ref("localhost:5000/foo/node:v1"),
            "localhost:5000/foo/node"
        );
    }

    #[test]
    fn repo_without_ref_digest_with_registry_port() {
        assert_eq!(
            repo_without_ref("localhost:5000/foo/node@sha256:AAA"),
            "localhost:5000/foo/node"
        );
    }

    #[test]
    fn repo_without_ref_bare_image_unchanged() {
        assert_eq!(repo_without_ref("quay.io/foo/node"), "quay.io/foo/node");
    }
}
