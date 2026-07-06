//! Real [`pipeline::Ops`] implementation.
//!
//! [`RealOps`] places confext DDI blobs under a confexts directory,
//! refreshes them via `systemd-confext`, reads/writes the bookkeeping
//! JSON file, and reconciles the booted OS image via `bootc`. All of
//! that is real filesystem I/O, but subprocess invocation sits behind
//! the injectable [`CommandRunner`] seam so tests never shell out to
//! `systemd-confext`/`bootc`.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};

use crate::pipeline::{Bookkeeping, BootcStatus, Ops, OpsError};

/// How many `<name>.raw` generations [`RealOps::place`] keeps under the
/// confexts directory; older ones are pruned. Mirrors the abandoned Go
/// agent's `keepGenerations = 2`.
const KEEP_GENERATIONS: usize = 2;

/// Sidecar file recording placement order (oldest first, one name per
/// line) alongside the `.raw` files. Pruning needs to know which
/// generation was placed longest ago; filesystem mtimes are too coarse
/// for that in practice (back-to-back writes during a test run land on
/// the exact same nanosecond mtime on this host's tmpfs), so order is
/// tracked explicitly instead of relying on stat().
const GENERATIONS_FILE: &str = ".generations";

/// Error from [`CommandRunner::run`]. `NotFound` means the program
/// itself couldn't be located/executed (e.g. the binary is missing);
/// `Failed` means it ran and exited non-zero, or some other execution
/// error occurred. Callers that need to distinguish "not installed"
/// from "ran and failed" (see [`RealOps::bootc_status`]) match on this.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RunError {
    NotFound(String),
    Failed(String),
}

impl std::fmt::Display for RunError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RunError::NotFound(msg) | RunError::Failed(msg) => write!(f, "{msg}"),
        }
    }
}

impl std::error::Error for RunError {}

fn to_ops_error(e: RunError) -> OpsError {
    match e {
        RunError::NotFound(msg) | RunError::Failed(msg) => OpsError(msg),
    }
}

/// Injectable seam for running external commands. Production code uses
/// [`RealCommandRunner`]; tests substitute a fake that records
/// program+args and returns configured output, without ever spawning a
/// process.
pub trait CommandRunner {
    /// Runs `program` with `args` to completion and returns its stdout
    /// on success (exit code 0).
    fn run(&mut self, program: &str, args: &[&str]) -> Result<String, RunError>;
}

/// Shells out via `std::process::Command`.
#[derive(Debug)]
pub struct RealCommandRunner;

impl CommandRunner for RealCommandRunner {
    fn run(&mut self, program: &str, args: &[&str]) -> Result<String, RunError> {
        let output = Command::new(program).args(args).output().map_err(|e| {
            if e.kind() == io::ErrorKind::NotFound {
                RunError::NotFound(format!("{program}: not found: {e}"))
            } else {
                RunError::Failed(format!("{program}: {e}"))
            }
        })?;
        if !output.status.success() {
            return Err(RunError::Failed(format!(
                "{program} {}: {}: {}",
                args.join(" "),
                output.status,
                String::from_utf8_lossy(&output.stderr)
            )));
        }
        Ok(String::from_utf8_lossy(&output.stdout).into_owned())
    }
}

/// Real [`Ops`] implementation, backed by real files under
/// `confexts_dir`/`bookkeeping_path` and a [`CommandRunner`] for
/// `systemd-confext`/`bootc`.
pub struct RealOps<R: CommandRunner> {
    confexts_dir: PathBuf,
    bookkeeping_path: PathBuf,
    runner: R,
}

impl RealOps<RealCommandRunner> {
    /// A `RealOps` that shells out to the real `systemd-confext`/`bootc`
    /// binaries.
    pub fn new(confexts_dir: impl Into<PathBuf>, bookkeeping_path: impl Into<PathBuf>) -> Self {
        Self::with_runner(confexts_dir, bookkeeping_path, RealCommandRunner)
    }
}

impl<R: CommandRunner> RealOps<R> {
    /// A `RealOps` backed by a caller-supplied [`CommandRunner`] (tests
    /// use this with a fake runner).
    pub fn with_runner(
        confexts_dir: impl Into<PathBuf>,
        bookkeeping_path: impl Into<PathBuf>,
        runner: R,
    ) -> Self {
        Self {
            confexts_dir: confexts_dir.into(),
            bookkeeping_path: bookkeeping_path.into(),
            runner,
        }
    }

    /// Records `name` as the most-recently-placed generation and
    /// removes `.raw` files for any generation beyond the
    /// [`KEEP_GENERATIONS`] most recent.
    fn prune_generations(&self, name: &str) -> io::Result<()> {
        let order_path = self.confexts_dir.join(GENERATIONS_FILE);
        let mut order = read_order(&order_path)?;
        order.retain(|n| n != name);
        order.push(name.to_string());

        let excess = order.len().saturating_sub(KEEP_GENERATIONS);
        for stale in order.drain(..excess) {
            let path = self.confexts_dir.join(format!("{stale}.raw"));
            match fs::remove_file(&path) {
                Ok(()) => {}
                Err(e) if e.kind() == io::ErrorKind::NotFound => {}
                Err(e) => return Err(e),
            }
        }

        write_order(&order_path, &order)
    }
}

impl<R: CommandRunner> Ops for RealOps<R> {
    fn place(&mut self, name: &str, blob: &[u8]) -> Result<(), OpsError> {
        fs::create_dir_all(&self.confexts_dir)
            .map_err(|e| OpsError(format!("create {}: {e}", self.confexts_dir.display())))?;

        let target = self.confexts_dir.join(format!("{name}.raw"));
        atomic_write(&target, blob)
            .map_err(|e| OpsError(format!("place {}: {e}", target.display())))?;

        self.prune_generations(name)
            .map_err(|e| OpsError(format!("prune confext generations: {e}")))
    }

    fn refresh(&mut self) -> Result<(), OpsError> {
        // --force ("ignore version incompatibilities", per systemd-confext
        // --help) is required for real merges to take effect at all on a
        // real bootc host: confirmed against systemd 259 on Fedora 44 that
        // an extension-release lacking a version field is NOT treated as
        // "no version check" -- it is rejected ("does not contain
        // VERSION_ID in release file but requested to match '44'"), and a
        // node cannot generally be expected to have already booted an OS
        // image whose /etc/os-release declares a matching SYSEXT_LEVEL for
        // whatever confext content this cycle delivers (config delivery
        // must work independent of image staging/reboot -- see
        // docs/nanokube/2026-07-06-step1-implementation-plan-rev5.md's
        // no-reboot-required design). --force only ignores the version
        // check, not ID matching (confirmed via --help's own wording,
        // scoped to "version incompatibilities"), so a confext built for
        // an unrelated OS ID is still rejected.
        self.runner
            .run("systemd-confext", &["refresh", "--mutable=yes", "--force"])
            .map(|_| ())
            .map_err(to_ops_error)
    }

    fn read_bookkeeping(&mut self) -> Result<Bookkeeping, OpsError> {
        let data = match fs::read(&self.bookkeeping_path) {
            Ok(d) => d,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Bookkeeping::default()),
            Err(e) => {
                return Err(OpsError(format!(
                    "read {}: {e}",
                    self.bookkeeping_path.display()
                )));
            }
        };
        let doc: BookkeepingDoc = serde_json::from_slice(&data)
            .map_err(|e| OpsError(format!("parse {}: {e}", self.bookkeeping_path.display())))?;
        Ok(doc.into())
    }

    fn write_bookkeeping(&mut self, bk: &Bookkeeping) -> Result<(), OpsError> {
        let doc = BookkeepingDoc::from(bk.clone());
        let data = serde_json::to_vec_pretty(&doc)
            .map_err(|e| OpsError(format!("encode bookkeeping: {e}")))?;
        atomic_write(&self.bookkeeping_path, &data)
            .map_err(|e| OpsError(format!("write {}: {e}", self.bookkeeping_path.display())))
    }

    fn bootc_status(&mut self) -> Result<Option<BootcStatus>, OpsError> {
        let stdout = match self
            .runner
            .run("bootc", &["status", "--json", "--format-version=1"])
        {
            Ok(s) => s,
            // A missing `bootc` binary means "not a bootc host": config
            // delivery already succeeded above, image staging is simply
            // skipped. A command that ran and failed, or produced
            // unparseable output, is a real error and propagates below.
            Err(RunError::NotFound(_)) => return Ok(None),
            Err(e) => return Err(to_ops_error(e)),
        };
        let root: StatusRoot = serde_json::from_str(&stdout)
            .map_err(|e| OpsError(format!("parse bootc status: {e}")))?;
        Ok(Some(root.into()))
    }

    fn bootc_switch(&mut self, image_ref: &str) -> Result<(), OpsError> {
        self.runner
            .run("bootc", &["switch", image_ref])
            .map(|_| ())
            .map_err(to_ops_error)
    }
}

/// Writes `data` to `path` such that a reader never observes a
/// half-written file: write to a temp file in the same directory, then
/// rename over `path` (an atomic replace on the same filesystem).
fn atomic_write(path: &Path, data: &[u8]) -> io::Result<()> {
    let dir = path.parent().unwrap_or_else(|| Path::new("."));
    fs::create_dir_all(dir)?;
    let file_name = path
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("nanokube-agent");
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    let tmp_path = dir.join(format!(".{file_name}.tmp.{}.{nonce}", std::process::id()));
    fs::write(&tmp_path, data)?;
    fs::rename(&tmp_path, path)?;
    Ok(())
}

fn read_order(path: &Path) -> io::Result<Vec<String>> {
    match fs::read_to_string(path) {
        Ok(s) => Ok(s.lines().map(str::to_string).collect()),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(Vec::new()),
        Err(e) => Err(e),
    }
}

fn write_order(path: &Path, order: &[String]) -> io::Result<()> {
    let mut data = order.join("\n");
    if !order.is_empty() {
        data.push('\n');
    }
    atomic_write(path, data.as_bytes())
}

/// Wire shape for the bookkeeping JSON file, matching the abandoned Go
/// agent's field names exactly (`expectedDigest`/`desiredName`).
///
/// Always emits both keys, including when empty: `pipeline::Bookkeeping`
/// already treats `""` as the normal "unset" value for both fields (see
/// its doc comment), and deserialization falls back to `""` via
/// `#[serde(default)]` regardless of whether the key was present at
/// all. So there's no information distinction between an omitted key
/// and an explicit `""` to preserve — always emitting both keeps the
/// on-disk format simple and predictable.
#[derive(Debug, Serialize, Deserialize)]
struct BookkeepingDoc {
    #[serde(default, rename = "expectedDigest")]
    expected_digest: String,
    #[serde(default, rename = "desiredName")]
    desired_name: String,
}

impl From<Bookkeeping> for BookkeepingDoc {
    fn from(bk: Bookkeeping) -> Self {
        BookkeepingDoc {
            expected_digest: bk.expected_digest,
            desired_name: bk.desired_name,
        }
    }
}

impl From<BookkeepingDoc> for Bookkeeping {
    fn from(doc: BookkeepingDoc) -> Self {
        Bookkeeping {
            expected_digest: doc.expected_digest,
            desired_name: doc.desired_name,
        }
    }
}

/// Deserialization target for `bootc status --json --format-version=1`,
/// reduced to what [`RealOps::bootc_status`] needs. bootc's real schema
/// nests three `"image"` keys: a status entry (`booted`/`staged`) wraps
/// an image-status object, which itself wraps an image-reference object
/// holding the pull-spec, alongside a sibling `imageDigest`.
#[derive(Debug, Deserialize)]
struct StatusRoot {
    status: StatusBody,
}

#[derive(Debug, Deserialize)]
struct StatusBody {
    booted: DeploymentStatus,
    staged: Option<DeploymentStatus>,
}

#[derive(Debug, Deserialize)]
struct DeploymentStatus {
    image: ImageStatus,
}

#[derive(Debug, Deserialize)]
struct ImageStatus {
    image: ImageReference,
    #[serde(rename = "imageDigest")]
    image_digest: String,
}

#[derive(Debug, Deserialize)]
struct ImageReference {
    image: String,
}

impl From<StatusRoot> for BootcStatus {
    fn from(root: StatusRoot) -> Self {
        let staged = root.status.staged;
        BootcStatus {
            booted_image: root.status.booted.image.image.image,
            booted_digest: root.status.booted.image.image_digest,
            staged_image: staged.as_ref().map(|s| s.image.image.image.clone()),
            staged_digest: staged.map(|s| s.image.image_digest),
        }
    }
}

#[cfg(test)]
mod tests {
    use std::collections::VecDeque;

    use tempfile::TempDir;

    use super::*;

    /// Records every `run` call's program+args and returns
    /// pre-configured responses in FIFO order. Panics if called more
    /// times than responses were queued — an unexpected extra call is
    /// as much a test failure as a wrong one.
    #[derive(Default)]
    struct FakeCommandRunner {
        calls: Vec<(String, Vec<String>)>,
        responses: VecDeque<Result<String, RunError>>,
    }

    impl FakeCommandRunner {
        fn new() -> Self {
            Self::default()
        }

        fn push(mut self, response: Result<String, RunError>) -> Self {
            self.responses.push_back(response);
            self
        }
    }

    impl CommandRunner for FakeCommandRunner {
        fn run(&mut self, program: &str, args: &[&str]) -> Result<String, RunError> {
            self.calls.push((
                program.to_string(),
                args.iter().map(|s| s.to_string()).collect(),
            ));
            self.responses
                .pop_front()
                .unwrap_or_else(|| panic!("unexpected command: {program} {args:?}"))
        }
    }

    fn call(program: &str, args: &[&str]) -> (String, Vec<String>) {
        (
            program.to_string(),
            args.iter().map(|s| s.to_string()).collect(),
        )
    }

    // --- place / generation pruning -----------------------------------

    #[test]
    fn place_writes_blob_and_prunes_to_two_generations() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("a", b"blob-a").unwrap();
        ops.place("b", b"blob-b").unwrap();
        ops.place("c", b"blob-c").unwrap();

        assert_eq!(raw_files(&confexts_dir), vec!["b.raw", "c.raw"]);
        assert_eq!(fs::read(confexts_dir.join("c.raw")).unwrap(), b"blob-c");
    }

    #[test]
    fn place_same_name_twice_does_not_evict_others() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("a", b"1").unwrap();
        ops.place("b", b"2").unwrap();
        ops.place("a", b"3").unwrap(); // re-place "a": must not evict "b"

        assert_eq!(raw_files(&confexts_dir), vec!["a.raw", "b.raw"]);
        assert_eq!(fs::read(confexts_dir.join("a.raw")).unwrap(), b"3");
    }

    fn raw_files(dir: &Path) -> Vec<String> {
        let mut names: Vec<String> = fs::read_dir(dir)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .filter(|n| n.ends_with(".raw"))
            .collect();
        names.sort();
        names
    }

    // --- refresh (bug 3 regression) --------------------------------

    #[test]
    fn refresh_argv_includes_mutable_yes_bug3_regression() {
        let dir = TempDir::new().unwrap();
        let runner = FakeCommandRunner::new().push(Ok(String::new()));
        let mut ops = RealOps::with_runner(dir.path(), dir.path().join("bk.json"), runner);

        ops.refresh().unwrap();

        assert_eq!(
            ops.runner.calls,
            vec![call(
                "systemd-confext",
                &["refresh", "--mutable=yes", "--force"]
            )]
        );
    }

    // --- bootc_status ------------------------------------------------

    #[test]
    fn bootc_status_parses_booted_without_staged() {
        let dir = TempDir::new().unwrap();
        let json = r#"{"status":{
            "booted":{"image":{"image":{"image":"quay.io/foo/node:v1"},"imageDigest":"sha256:AAA"}},
            "staged":null
        }}"#;
        let runner = FakeCommandRunner::new().push(Ok(json.to_string()));
        let mut ops = RealOps::with_runner(dir.path(), dir.path().join("bk.json"), runner);

        let status = ops.bootc_status().unwrap().unwrap();

        assert_eq!(
            status,
            BootcStatus {
                booted_image: "quay.io/foo/node:v1".to_string(),
                booted_digest: "sha256:AAA".to_string(),
                staged_image: None,
                staged_digest: None,
            }
        );
        assert_eq!(
            ops.runner.calls,
            vec![call("bootc", &["status", "--json", "--format-version=1"])]
        );
    }

    #[test]
    fn bootc_status_parses_booted_with_staged() {
        let dir = TempDir::new().unwrap();
        let json = r#"{"status":{
            "booted":{"image":{"image":{"image":"quay.io/foo/node:v1"},"imageDigest":"sha256:AAA"}},
            "staged":{"image":{"image":{"image":"quay.io/foo/node:v2"},"imageDigest":"sha256:BBB"}}
        }}"#;
        let runner = FakeCommandRunner::new().push(Ok(json.to_string()));
        let mut ops = RealOps::with_runner(dir.path(), dir.path().join("bk.json"), runner);

        let status = ops.bootc_status().unwrap().unwrap();

        assert_eq!(
            status,
            BootcStatus {
                booted_image: "quay.io/foo/node:v1".to_string(),
                booted_digest: "sha256:AAA".to_string(),
                staged_image: Some("quay.io/foo/node:v2".to_string()),
                staged_digest: Some("sha256:BBB".to_string()),
            }
        );
    }

    #[test]
    fn bootc_status_returns_none_when_binary_missing() {
        let dir = TempDir::new().unwrap();
        let runner =
            FakeCommandRunner::new().push(Err(RunError::NotFound("bootc: not found".to_string())));
        let mut ops = RealOps::with_runner(dir.path(), dir.path().join("bk.json"), runner);

        assert_eq!(ops.bootc_status().unwrap(), None);
    }

    #[test]
    fn bootc_status_propagates_bad_json_as_error() {
        let dir = TempDir::new().unwrap();
        let runner = FakeCommandRunner::new().push(Ok("not json".to_string()));
        let mut ops = RealOps::with_runner(dir.path(), dir.path().join("bk.json"), runner);

        assert!(ops.bootc_status().is_err());
    }

    #[test]
    fn bootc_status_propagates_command_failure_as_error() {
        let dir = TempDir::new().unwrap();
        let runner =
            FakeCommandRunner::new().push(Err(RunError::Failed("bootc: exit 1".to_string())));
        let mut ops = RealOps::with_runner(dir.path(), dir.path().join("bk.json"), runner);

        assert!(ops.bootc_status().is_err());
    }

    // --- bootc_switch --------------------------------------------------

    #[test]
    fn bootc_switch_invokes_with_exact_image_ref() {
        let dir = TempDir::new().unwrap();
        let runner = FakeCommandRunner::new().push(Ok(String::new()));
        let mut ops = RealOps::with_runner(dir.path(), dir.path().join("bk.json"), runner);

        ops.bootc_switch("quay.io/foo/node@sha256:AAA").unwrap();

        assert_eq!(
            ops.runner.calls,
            vec![call("bootc", &["switch", "quay.io/foo/node@sha256:AAA"])]
        );
    }

    // --- bookkeeping ---------------------------------------------------

    #[test]
    fn bookkeeping_round_trips() {
        let dir = TempDir::new().unwrap();
        let bk_path = dir.path().join("bookkeeping.json");
        let mut ops = RealOps::with_runner(dir.path(), &bk_path, FakeCommandRunner::new());
        let bk = Bookkeeping {
            expected_digest: "sha256:AAA".to_string(),
            desired_name: "v1".to_string(),
        };

        ops.write_bookkeeping(&bk).unwrap();

        assert_eq!(ops.read_bookkeeping().unwrap(), bk);
    }

    #[test]
    fn read_bookkeeping_missing_file_returns_default() {
        let dir = TempDir::new().unwrap();
        let bk_path = dir.path().join("does-not-exist.json");
        let mut ops = RealOps::with_runner(dir.path(), &bk_path, FakeCommandRunner::new());

        assert_eq!(ops.read_bookkeeping().unwrap(), Bookkeeping::default());
    }

    #[test]
    fn bookkeeping_json_keys_are_exactly_expected_digest_and_desired_name() {
        let dir = TempDir::new().unwrap();
        let bk_path = dir.path().join("bookkeeping.json");
        let mut ops = RealOps::with_runner(dir.path(), &bk_path, FakeCommandRunner::new());

        ops.write_bookkeeping(&Bookkeeping {
            expected_digest: "sha256:AAA".to_string(),
            desired_name: "v1".to_string(),
        })
        .unwrap();

        // Parse as generic JSON (not our own struct) so a serde rename
        // mistake would actually be caught here instead of round-tripping
        // silently through matching (mis)configured field names.
        let raw = fs::read_to_string(&bk_path).unwrap();
        let value: serde_json::Value = serde_json::from_str(&raw).unwrap();
        assert_eq!(value["expectedDigest"], "sha256:AAA");
        assert_eq!(value["desiredName"], "v1");
        assert_eq!(value.as_object().unwrap().len(), 2);
    }

    #[test]
    fn write_bookkeeping_never_leaves_a_half_written_file_visible() {
        // Not a concurrency test (this crate has no async story yet) —
        // just confirms the write goes through atomic_write's temp file
        // rather than a direct truncating write, by checking no stray
        // temp file is left behind afterwards.
        let dir = TempDir::new().unwrap();
        let bk_path = dir.path().join("bookkeeping.json");
        let mut ops = RealOps::with_runner(dir.path(), &bk_path, FakeCommandRunner::new());

        ops.write_bookkeeping(&Bookkeeping::default()).unwrap();

        let entries: Vec<_> = fs::read_dir(dir.path()).unwrap().collect();
        assert_eq!(
            entries.len(),
            1,
            "expected only the final file, no leftover temp file"
        );
    }
}
