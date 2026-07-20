//! `apply --once` support: reads a `DesiredMetadata` JSON sidecar plus
//! its sibling `<name>.raw` blob from disk, then runs the same `apply()`
//! pipeline the gRPC server uses.

use std::fmt;
use std::fs;
use std::path::Path;

use serde::Deserialize;

use crate::pipeline::{ApplyError, DesiredMetadata, Ops, apply};

/// Wire shape of the `<name>.json` sidecar: protojson's default camelCase
/// output for `contract.desired.v1.DesiredMetadata` (see
/// `contract/fixtures/*.json`).
#[derive(Debug, Deserialize)]
struct DesiredMetadataJson {
    name: String,
    #[serde(rename = "blobSha256")]
    blob_sha256: String,
}

impl From<DesiredMetadataJson> for DesiredMetadata {
    fn from(j: DesiredMetadataJson) -> Self {
        DesiredMetadata {
            name: j.name,
            blob_sha256: j.blob_sha256,
        }
    }
}

/// Why [`apply_once`] failed.
#[derive(Debug)]
pub enum ApplyOnceError {
    /// Reading the `.json` sidecar or its sibling `.raw` blob failed.
    Io(String),
    /// The `.json` sidecar's content couldn't be parsed as
    /// `DesiredMetadata`.
    Json(String),
    /// The pipeline itself rejected or failed to apply the document.
    Apply(ApplyError),
}

impl fmt::Display for ApplyOnceError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ApplyOnceError::Io(msg) | ApplyOnceError::Json(msg) => write!(f, "{msg}"),
            ApplyOnceError::Apply(e) => write!(f, "{e}"),
        }
    }
}

impl std::error::Error for ApplyOnceError {}

/// Reads `desired_json_path` (a `<name>.json` sidecar) and its sibling
/// `<name>.raw` blob from the same directory, then applies them via
/// [`apply`]. Returns the applied document's name on success.
pub fn apply_once(desired_json_path: &Path, ops: &mut dyn Ops) -> Result<String, ApplyOnceError> {
    let json_data = fs::read(desired_json_path)
        .map_err(|e| ApplyOnceError::Io(format!("read {}: {e}", desired_json_path.display())))?;
    let meta: DesiredMetadataJson = serde_json::from_slice(&json_data)
        .map_err(|e| ApplyOnceError::Json(format!("parse {}: {e}", desired_json_path.display())))?;
    let meta: DesiredMetadata = meta.into();

    let raw_path = desired_json_path.with_extension("raw");
    let blob = fs::read(&raw_path)
        .map_err(|e| ApplyOnceError::Io(format!("read {}: {e}", raw_path.display())))?;

    apply(&meta, &blob, ops).map_err(ApplyOnceError::Apply)?;
    Ok(meta.name)
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use sha2::{Digest, Sha256};

    use super::*;
    use crate::pipeline::{Bookkeeping, BootcStatus, OpsError};

    /// The one committed golden fixture pair under `contract/fixtures/`.
    ///
    /// Discovers the fixture by scanning for the single `*.json` file in
    /// the directory instead of hardcoding its (content-derived) name, so
    /// this test doesn't go stale whenever the fixture is regenerated.
    fn fixture_json_path() -> PathBuf {
        let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../contract/fixtures");
        let candidates: Vec<PathBuf> = fs::read_dir(&dir)
            .unwrap_or_else(|e| panic!("read dir {}: {e}", dir.display()))
            .filter_map(|entry| entry.ok())
            .map(|entry| entry.path())
            .filter(|path| path.extension().and_then(|ext| ext.to_str()) == Some("json"))
            .collect();

        match candidates.as_slice() {
            [only] => only.clone(),
            [] => panic!("no *.json fixture found in {}", dir.display()),
            _ => panic!(
                "expected exactly one *.json fixture in {}, found {}: {candidates:?}",
                dir.display(),
                candidates.len()
            ),
        }
    }

    #[derive(Default)]
    struct FakeOps {
        calls: Vec<String>,
    }

    impl Ops for FakeOps {
        fn place(&mut self, name: &str, _blob: &[u8]) -> Result<(), OpsError> {
            self.calls.push(format!("place:{name}"));
            Ok(())
        }

        fn archive_previous(
            &mut self,
            current_name: &str,
            previous_name: &str,
        ) -> Result<(), OpsError> {
            self.calls
                .push(format!("archive_previous:{current_name}:{previous_name}"));
            Ok(())
        }

        fn refresh(&mut self) -> Result<(), OpsError> {
            self.calls.push("refresh".to_string());
            Ok(())
        }

        fn kubelet_is_active(&mut self) -> Result<bool, OpsError> {
            self.calls.push("kubelet_is_active".to_string());
            Ok(false)
        }

        fn read_bookkeeping(&mut self) -> Result<Bookkeeping, OpsError> {
            self.calls.push("read_bookkeeping".to_string());
            Ok(Bookkeeping::default())
        }

        fn write_bookkeeping(&mut self, bk: &Bookkeeping) -> Result<(), OpsError> {
            self.calls
                .push(format!("write_bookkeeping:{}", bk.desired_name));
            Ok(())
        }

        fn bootc_status(&mut self) -> Result<Option<BootcStatus>, OpsError> {
            self.calls.push("bootc_status".to_string());
            Ok(None)
        }

        fn bootc_switch(&mut self, image_ref: &str) -> Result<(), OpsError> {
            self.calls.push(format!("bootc_switch:{image_ref}"));
            Ok(())
        }
    }

    fn hex_encode(bytes: &[u8]) -> String {
        bytes.iter().map(|b| format!("{b:02x}")).collect()
    }

    #[test]
    fn parses_real_fixture_and_verifies_checksum_against_sibling_raw() {
        let json_path = fixture_json_path();
        let name = json_path.file_stem().unwrap().to_str().unwrap().to_string();

        // Parse straight off the real fixture (not synthetic data): if
        // this doesn't match, the parsing logic is wrong, not the
        // fixture (contract/fixtures/fixtures_test.go already enforces
        // the fixture's internal consistency on the Go side).
        let json_data = fs::read(&json_path).expect("read fixture .json");
        let meta: DesiredMetadataJson = serde_json::from_slice(&json_data).unwrap();
        assert_eq!(
            meta.name, name,
            "metadata name must match the fixture filename"
        );

        let raw_path = json_path.with_extension("raw");
        let raw = fs::read(&raw_path).expect("read fixture .raw");
        let got_sha = hex_encode(Sha256::digest(&raw).as_slice());
        assert_eq!(
            got_sha, meta.blob_sha256,
            "sha256(fixture .raw) must match its .json sidecar's blobSha256"
        );

        let mut ops = FakeOps::default();
        let applied_name = apply_once(&json_path, &mut ops).unwrap();

        assert_eq!(applied_name, name);
        assert!(ops.calls.contains(&format!("place:{name}")));
    }

    #[test]
    fn missing_raw_sibling_is_an_io_error() {
        let dir = tempfile::TempDir::new().unwrap();
        let json_path = dir.path().join("no-such-name.json");
        fs::write(
            &json_path,
            br#"{"name":"no-such-name","blobSha256":"deadbeef"}"#,
        )
        .unwrap();

        let mut ops = FakeOps::default();
        let err = apply_once(&json_path, &mut ops).unwrap_err();

        assert!(matches!(err, ApplyOnceError::Io(_)));
        assert!(ops.calls.is_empty());
    }
}
