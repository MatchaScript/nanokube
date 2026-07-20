//! Real [`pipeline::Ops`] implementation.
//!
//! [`RealOps`] places confext DDI blobs under a confexts directory,
//! refreshes them via `systemd-confext`, reads/writes the bookkeeping
//! JSON file, and reconciles the booted OS image via `bootc`. All of
//! that is real filesystem I/O, but subprocess invocation sits behind
//! the injectable [`CommandRunner`] seam so tests never shell out to
//! `systemd-confext`/`bootc`. `kubelet.service`'s state is instead read
//! straight off `org.freedesktop.systemd1` over D-Bus (see
//! [`SystemdBus`]), behind its own injectable seam so tests never touch
//! a real system bus either.

use std::fs;
use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};

use crate::pipeline::{Bookkeeping, BootcStatus, Ops, OpsError};

/// Name of the sidecar an older agent build wrote under the confexts
/// directory to track a multi-generation retention scheme (abandoned in
/// favor of [`RealOps::archive_previous`]'s single-image invariant). No
/// longer written; [`RealOps::place`] removes one opportunistically if a
/// node still carries it from before an upgrade.
const LEGACY_GENERATIONS_FILE: &str = ".generations";

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

/// `ActiveState` + `LoadState` of a systemd unit, as reported by the
/// `org.freedesktop.systemd1.Unit` D-Bus interface's properties of the
/// same names.
#[derive(Debug, Clone, PartialEq, Eq)]
struct UnitState {
    active_state: String,
    load_state: String,
}

/// Error querying a unit's state over D-Bus: connecting to the bus,
/// calling `LoadUnit`, or reading a property failed. Mirrors
/// [`RunError`]'s role for [`CommandRunner`] -- a "couldn't ask" signal
/// that must never be conflated with a legitimate answer.
#[derive(Debug, Clone, PartialEq, Eq)]
struct DbusError(String);

impl std::fmt::Display for DbusError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}

impl std::error::Error for DbusError {}

/// Injectable seam for querying a systemd unit's state over D-Bus.
/// Production code uses [`RealSystemdBus`]; tests substitute a fake that
/// records the queried unit name and returns a configured response,
/// without ever touching a real bus.
trait SystemdBus {
    fn unit_state(&mut self, unit: &str) -> Result<UnitState, DbusError>;
}

/// Manager object (fixed path/service), the entry point for resolving a
/// unit name to its own object path.
#[zbus::proxy(
    interface = "org.freedesktop.systemd1.Manager",
    default_service = "org.freedesktop.systemd1",
    default_path = "/org/freedesktop/systemd1",
    gen_blocking = false
)]
trait Manager {
    /// Resolves `name` to its unit object path, loading the unit from
    /// disk first if systemd hasn't already loaded it this boot (unlike
    /// `GetUnit`, which only succeeds for a unit systemd has already
    /// loaded/referenced -- see [`RealSystemdBus::unit_state`]).
    #[zbus(name = "LoadUnit")]
    fn load_unit(&self, name: &str) -> zbus::Result<zbus::zvariant::OwnedObjectPath>;
}

/// A unit object at a path resolved by [`ManagerProxy::load_unit`].
#[zbus::proxy(
    interface = "org.freedesktop.systemd1.Unit",
    default_service = "org.freedesktop.systemd1",
    gen_blocking = false
)]
trait Unit {
    #[zbus(property)]
    fn active_state(&self) -> zbus::Result<String>;
    #[zbus(property)]
    fn load_state(&self) -> zbus::Result<String>;
}

/// Runs `fut` to completion on the calling thread, whether or not a
/// tokio runtime is already active there.
///
/// [`RealOps::kubelet_is_active`] is a plain synchronous `Ops` method (a
/// seam requirement this D-Bus rewrite doesn't get to change), but it's
/// invoked from two different contexts: already inside the gRPC
/// server's tokio runtime (`server.rs`'s `push_desired`), and from
/// `apply --once`'s plain `fn main`, which never starts a runtime at
/// all. `zbus::blocking` (even with the `tokio` feature) keeps its own
/// separate background runtime and drives it with a plain
/// `Runtime::block_on`, which panics ("Cannot start a runtime from
/// within a runtime") if the calling thread is already inside a
/// runtime -- zbus's own docs call this the "async sandwich" footgun.
/// `block_in_place` is tokio's documented way out: it hands the current
/// task off to a spare worker thread so `Handle::block_on` can reenter
/// the *same* runtime instead of starting a second one.
fn run_async<F: std::future::Future>(fut: F) -> F::Output {
    match tokio::runtime::Handle::try_current() {
        Ok(handle) => tokio::task::block_in_place(|| handle.block_on(fut)),
        Err(_) => tokio::runtime::Runtime::new()
            .expect("start tokio runtime for D-Bus query")
            .block_on(fut),
    }
}

/// Queries the real system D-Bus via `org.freedesktop.systemd1`. A
/// fresh connection is opened per call, mirroring [`RealCommandRunner`]
/// spawning a fresh process per call.
struct RealSystemdBus;

impl SystemdBus for RealSystemdBus {
    fn unit_state(&mut self, unit: &str) -> Result<UnitState, DbusError> {
        run_async(async {
            let connection = zbus::Connection::system()
                .await
                .map_err(|e| DbusError(format!("connect to system bus: {e}")))?;

            let manager = ManagerProxy::new(&connection)
                .await
                .map_err(|e| DbusError(format!("build Manager proxy: {e}")))?;
            let unit_path = manager
                .load_unit(unit)
                .await
                .map_err(|e| DbusError(format!("LoadUnit {unit}: {e}")))?;

            let unit_proxy = UnitProxy::builder(&connection)
                .path(unit_path)
                .map_err(|e| DbusError(format!("Unit proxy path for {unit}: {e}")))?
                .build()
                .await
                .map_err(|e| DbusError(format!("build Unit proxy for {unit}: {e}")))?;
            let active_state = unit_proxy
                .active_state()
                .await
                .map_err(|e| DbusError(format!("get {unit} ActiveState: {e}")))?;
            let load_state = unit_proxy
                .load_state()
                .await
                .map_err(|e| DbusError(format!("get {unit} LoadState: {e}")))?;

            Ok(UnitState {
                active_state,
                load_state,
            })
        })
    }
}

const KUBELET_UNIT: &str = "kubelet.service";

/// Reduces `unit`'s D-Bus state to the `Ops::kubelet_is_active` boolean.
///
/// D-Bus instead of shelling out to `systemctl is-active` for two
/// reasons:
/// 1. `systemctl` itself failing to reach systemd (e.g. "Failed to
///    connect to bus") exits non-zero exactly like a genuine "inactive"
///    answer, so the CLI-based check couldn't tell "couldn't ask" from
///    "asked, and the answer is no". A D-Bus connection/call failure
///    here surfaces as `Err` instead of silently reading as `Ok(false)`.
/// 2. `is-active`'s exit code only reflects `ActiveState`; it can't
///    distinguish a cleanly stopped unit from one that's masked
///    (`LoadState=masked`) or has no unit file at all
///    (`LoadState=not-found`) -- all three exit non-zero identically.
///    Reading `LoadState` alongside `ActiveState` lets the log line
///    below tell a human which of those actually happened.
fn kubelet_state(bus: &mut impl SystemdBus) -> Result<bool, OpsError> {
    let state = bus
        .unit_state(KUBELET_UNIT)
        .map_err(|e| OpsError(format!("query {KUBELET_UNIT} over D-Bus: {e}")))?;
    let active = state.active_state == "active";
    if !active {
        eprintln!(
            "{KUBELET_UNIT}: ActiveState={} LoadState={}",
            state.active_state, state.load_state
        );
    }
    Ok(active)
}

/// Real [`Ops`] implementation, backed by real files under
/// `confexts_dir`/`archive_dir`/`bookkeeping_path` and a
/// [`CommandRunner`] for `systemd-confext`/`bootc`.
pub struct RealOps<R: CommandRunner> {
    confexts_dir: PathBuf,
    archive_dir: PathBuf,
    bookkeeping_path: PathBuf,
    runner: R,
}

impl RealOps<RealCommandRunner> {
    /// A `RealOps` that shells out to the real `systemd-confext`/`bootc`
    /// binaries.
    pub fn new(
        confexts_dir: impl Into<PathBuf>,
        archive_dir: impl Into<PathBuf>,
        bookkeeping_path: impl Into<PathBuf>,
    ) -> Self {
        Self::with_runner(
            confexts_dir,
            archive_dir,
            bookkeeping_path,
            RealCommandRunner,
        )
    }
}

impl<R: CommandRunner> RealOps<R> {
    /// A `RealOps` backed by a caller-supplied [`CommandRunner`] (tests
    /// use this with a fake runner).
    pub fn with_runner(
        confexts_dir: impl Into<PathBuf>,
        archive_dir: impl Into<PathBuf>,
        bookkeeping_path: impl Into<PathBuf>,
        runner: R,
    ) -> Self {
        Self {
            confexts_dir: confexts_dir.into(),
            archive_dir: archive_dir.into(),
            bookkeeping_path: bookkeeping_path.into(),
            runner,
        }
    }
}

impl<R: CommandRunner> Ops for RealOps<R> {
    fn place(&mut self, name: &str, blob: &[u8]) -> Result<(), OpsError> {
        fs::create_dir_all(&self.confexts_dir)
            .map_err(|e| OpsError(format!("create {}: {e}", self.confexts_dir.display())))?;

        let target = self.confexts_dir.join(format!("{name}.raw"));
        atomic_write(&target, blob)
            .map_err(|e| OpsError(format!("place {}: {e}", target.display())))?;
        fs::set_permissions(&target, fs::Permissions::from_mode(0o600))
            .map_err(|e| OpsError(format!("chmod {}: {e}", target.display())))?;

        match fs::remove_file(self.confexts_dir.join(LEGACY_GENERATIONS_FILE)) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(OpsError(format!(
                "remove stale {LEGACY_GENERATIONS_FILE}: {e}"
            ))),
        }
    }

    /// Uniform rule for every `.raw` left in `confexts_dir` besides
    /// `current_name`'s (there is normally at most one — the previous
    /// generation — but a node upgraded from the old multi-generation
    /// scheme may still carry an extra one): if it matches
    /// `previous_name`, move it into the archive (replacing whatever was
    /// archived before); otherwise delete it as an orphan.
    fn archive_previous(
        &mut self,
        current_name: &str,
        previous_name: &str,
    ) -> Result<(), OpsError> {
        fs::create_dir_all(&self.archive_dir)
            .map_err(|e| OpsError(format!("create {}: {e}", self.archive_dir.display())))?;
        fs::set_permissions(&self.archive_dir, fs::Permissions::from_mode(0o700))
            .map_err(|e| OpsError(format!("chmod {}: {e}", self.archive_dir.display())))?;

        let extras = other_raw_names(&self.confexts_dir, current_name)
            .map_err(|e| OpsError(format!("scan {}: {e}", self.confexts_dir.display())))?;

        for name in extras {
            let src = self.confexts_dir.join(format!("{name}.raw"));
            if !previous_name.is_empty() && name == previous_name {
                clear_archive(&self.archive_dir)
                    .map_err(|e| OpsError(format!("clear {}: {e}", self.archive_dir.display())))?;
                let dst = self.archive_dir.join(format!("{name}.raw"));
                fs::rename(&src, &dst)
                    .map_err(|e| OpsError(format!("archive {}: {e}", src.display())))?;
            } else {
                fs::remove_file(&src)
                    .map_err(|e| OpsError(format!("remove stale {}: {e}", src.display())))?;
            }
        }
        Ok(())
    }

    fn refresh(&mut self) -> Result<(), OpsError> {
        self.runner
            .run("systemd-confext", &["refresh", "--mutable=yes"])
            .map(|_| ())
            .map_err(to_ops_error)
    }

    fn kubelet_is_active(&mut self) -> Result<bool, OpsError> {
        kubelet_state(&mut RealSystemdBus)
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

/// Names (without the `.raw` suffix) of every `<name>.raw` file directly
/// under `dir` except `exclude`'s.
fn other_raw_names(dir: &Path, exclude: &str) -> io::Result<Vec<String>> {
    let entries = match fs::read_dir(dir) {
        Ok(entries) => entries,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e),
    };
    let mut names = Vec::new();
    for entry in entries {
        let file_name = entry?.file_name();
        if let Some(name) = file_name.to_str().and_then(|n| n.strip_suffix(".raw"))
            && name != exclude
        {
            names.push(name.to_string());
        }
    }
    Ok(names)
}

/// Removes every `*.raw` file directly under `archive_dir`, keeping the
/// one-generation retention invariant when a new generation is about to
/// be archived in.
fn clear_archive(archive_dir: &Path) -> io::Result<()> {
    for entry in fs::read_dir(archive_dir)? {
        let entry = entry?;
        if entry
            .file_name()
            .to_str()
            .is_some_and(|n| n.ends_with(".raw"))
        {
            fs::remove_file(entry.path())?;
        }
    }
    Ok(())
}

/// Wire shape for the bookkeeping JSON file, matching the abandoned Go
/// agent's field name exactly (`desiredName`).
///
/// Always emits `desiredName`, including when empty:
/// `pipeline::Bookkeeping` already treats `""` as the normal "unset"
/// value (see its doc comment), and deserialization falls back to `""`
/// via `#[serde(default)]` regardless of whether the key was present at
/// all. So there's no information distinction between an omitted key
/// and an explicit `""` to preserve — always emitting the key keeps the
/// on-disk format simple and predictable.
#[derive(Debug, Serialize, Deserialize)]
struct BookkeepingDoc {
    #[serde(default, rename = "desiredName")]
    desired_name: String,
}

impl From<Bookkeeping> for BookkeepingDoc {
    fn from(bk: Bookkeeping) -> Self {
        BookkeepingDoc {
            desired_name: bk.desired_name,
        }
    }
}

impl From<BookkeepingDoc> for Bookkeeping {
    fn from(doc: BookkeepingDoc) -> Self {
        Bookkeeping {
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

    // --- place ----------------------------------------------------------

    #[test]
    fn place_writes_blob_with_mode_0600() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("a", b"blob-a").unwrap();

        assert_eq!(raw_files(&confexts_dir), vec!["a.raw"]);
        assert_eq!(fs::read(confexts_dir.join("a.raw")).unwrap(), b"blob-a");
        assert_eq!(file_mode(&confexts_dir.join("a.raw")), 0o600);
    }

    #[test]
    fn place_does_not_touch_other_raw_files() {
        // Pruning down to a single generation is archive_previous's job
        // now, not place's — place only ever writes its own blob.
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        fs::create_dir_all(&confexts_dir).unwrap();
        fs::write(confexts_dir.join("other.raw"), b"unrelated").unwrap();
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("new", b"blob-new").unwrap();

        assert_eq!(raw_files(&confexts_dir), vec!["new.raw", "other.raw"]);
    }

    #[test]
    fn place_removes_a_stale_legacy_generations_file() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        fs::create_dir_all(&confexts_dir).unwrap();
        fs::write(confexts_dir.join(LEGACY_GENERATIONS_FILE), b"a\nb\n").unwrap();
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("new", b"blob-new").unwrap();

        assert!(!confexts_dir.join(LEGACY_GENERATIONS_FILE).exists());
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

    fn file_mode(path: &Path) -> u32 {
        fs::metadata(path).unwrap().permissions().mode() & 0o777
    }

    // --- archive_previous ------------------------------------------------

    #[test]
    fn first_apply_creates_archive_dir_mode_0700_and_archives_nothing() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let archive_dir = dir.path().join("archive");
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            &archive_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("a", b"blob-a").unwrap();
        ops.archive_previous("a", "").unwrap(); // no previous bookkeeping yet

        assert_eq!(raw_files(&confexts_dir), vec!["a.raw"]);
        assert!(raw_files(&archive_dir).is_empty());
        assert_eq!(file_mode(&archive_dir), 0o700);
    }

    #[test]
    fn archive_previous_moves_the_matching_generation_and_confexts_stays_single_image() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let archive_dir = dir.path().join("archive");
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            &archive_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("a", b"blob-a").unwrap();
        ops.archive_previous("a", "").unwrap();
        ops.place("b", b"blob-b").unwrap();
        ops.archive_previous("b", "a").unwrap();

        assert_eq!(raw_files(&confexts_dir), vec!["b.raw"]);
        assert_eq!(raw_files(&archive_dir), vec!["a.raw"]);
        assert_eq!(fs::read(archive_dir.join("a.raw")).unwrap(), b"blob-a");
        assert_eq!(file_mode(&archive_dir.join("a.raw")), 0o600);
    }

    #[test]
    fn second_update_replaces_the_archived_generation() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let archive_dir = dir.path().join("archive");
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            &archive_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("a", b"blob-a").unwrap();
        ops.archive_previous("a", "").unwrap();
        ops.place("b", b"blob-b").unwrap();
        ops.archive_previous("b", "a").unwrap();
        ops.place("c", b"blob-c").unwrap();
        ops.archive_previous("c", "b").unwrap();

        assert_eq!(raw_files(&confexts_dir), vec!["c.raw"]);
        assert_eq!(raw_files(&archive_dir), vec!["b.raw"]);
    }

    #[test]
    fn repushing_the_same_name_leaves_the_archive_unchanged() {
        // Mirrors apply()'s own no-op skip for a re-push of the current
        // revision, at the Ops level: calling archive_previous with the
        // just-placed name as its own previous is not a real transition.
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let archive_dir = dir.path().join("archive");
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            &archive_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("a", b"blob-a").unwrap();
        ops.archive_previous("a", "").unwrap();
        ops.place("a", b"blob-a-again").unwrap();
        ops.archive_previous("a", "a").unwrap();

        assert_eq!(raw_files(&confexts_dir), vec!["a.raw"]);
        assert!(raw_files(&archive_dir).is_empty());
    }

    #[test]
    fn migration_from_two_raws_archives_the_bookkept_one_and_deletes_the_other() {
        // A node upgraded from the old KEEP_GENERATIONS=2 scheme may
        // still carry two raws when the first post-upgrade push lands.
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let archive_dir = dir.path().join("archive");
        fs::create_dir_all(&confexts_dir).unwrap();
        fs::write(confexts_dir.join("old1.raw"), b"stale-unbookkept").unwrap();
        fs::write(confexts_dir.join("old2.raw"), b"stale-bookkept").unwrap();
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            &archive_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("new", b"blob-new").unwrap();
        ops.archive_previous("new", "old2").unwrap(); // "old2" is bookkeeping's last-applied name

        assert_eq!(raw_files(&confexts_dir), vec!["new.raw"]);
        assert_eq!(raw_files(&archive_dir), vec!["old2.raw"]);
        assert_eq!(
            fs::read(archive_dir.join("old2.raw")).unwrap(),
            b"stale-bookkept"
        );
    }

    #[test]
    fn unidentifiable_previous_name_archives_nothing_and_deletes_the_extras() {
        let dir = TempDir::new().unwrap();
        let confexts_dir = dir.path().join("confexts");
        let archive_dir = dir.path().join("archive");
        fs::create_dir_all(&confexts_dir).unwrap();
        fs::write(confexts_dir.join("stray1.raw"), b"x").unwrap();
        fs::write(confexts_dir.join("stray2.raw"), b"y").unwrap();
        let mut ops = RealOps::with_runner(
            &confexts_dir,
            &archive_dir,
            dir.path().join("bk.json"),
            FakeCommandRunner::new(),
        );

        ops.place("new", b"blob-new").unwrap();
        // "unknown" matches neither stray file.
        ops.archive_previous("new", "unknown").unwrap();

        assert_eq!(raw_files(&confexts_dir), vec!["new.raw"]);
        assert!(raw_files(&archive_dir).is_empty());
    }

    // --- kubelet_state (D-Bus) --------------------------------------------

    /// Records the queried unit name and returns a configured response,
    /// without ever touching a real bus.
    struct FakeSystemdBus {
        calls: Vec<String>,
        response: Result<UnitState, DbusError>,
    }

    impl FakeSystemdBus {
        fn new(response: Result<UnitState, DbusError>) -> Self {
            Self {
                calls: Vec::new(),
                response,
            }
        }
    }

    impl SystemdBus for FakeSystemdBus {
        fn unit_state(&mut self, unit: &str) -> Result<UnitState, DbusError> {
            self.calls.push(unit.to_string());
            self.response.clone()
        }
    }

    fn unit_state(active_state: &str, load_state: &str) -> UnitState {
        UnitState {
            active_state: active_state.to_string(),
            load_state: load_state.to_string(),
        }
    }

    #[test]
    fn kubelet_state_true_when_active_state_is_active() {
        let mut bus = FakeSystemdBus::new(Ok(unit_state("active", "loaded")));

        assert!(kubelet_state(&mut bus).unwrap());
        assert_eq!(bus.calls, vec![KUBELET_UNIT.to_string()]);
    }

    #[test]
    fn kubelet_state_false_when_cleanly_inactive() {
        let mut bus = FakeSystemdBus::new(Ok(unit_state("inactive", "loaded")));

        assert!(!kubelet_state(&mut bus).unwrap());
    }

    #[test]
    fn kubelet_state_false_when_masked() {
        // LoadState=masked (unit symlinked to /dev/null, cannot be
        // started) reads as "not active" just like a clean stop -- the
        // distinction survives only in the log line, not the boolean.
        let mut bus = FakeSystemdBus::new(Ok(unit_state("inactive", "masked")));

        assert!(!kubelet_state(&mut bus).unwrap());
    }

    #[test]
    fn kubelet_state_false_when_not_found() {
        // LoadState=not-found (no unit file at all) likewise reads as
        // "not active", not an error: LoadUnit itself succeeded.
        let mut bus = FakeSystemdBus::new(Ok(unit_state("inactive", "not-found")));

        assert!(!kubelet_state(&mut bus).unwrap());
    }

    #[test]
    fn kubelet_state_propagates_dbus_failure_as_ops_error() {
        let mut bus =
            FakeSystemdBus::new(Err(DbusError("connect to system bus: boom".to_string())));

        assert!(kubelet_state(&mut bus).is_err());
    }

    // --- refresh (bug 3 regression) --------------------------------

    #[test]
    fn refresh_argv_includes_mutable_yes_bug3_regression() {
        let dir = TempDir::new().unwrap();
        let runner = FakeCommandRunner::new().push(Ok(String::new()));
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            runner,
        );

        ops.refresh().unwrap();

        assert_eq!(
            ops.runner.calls,
            vec![call("systemd-confext", &["refresh", "--mutable=yes"])]
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
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            runner,
        );

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
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            runner,
        );

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
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            runner,
        );

        assert_eq!(ops.bootc_status().unwrap(), None);
    }

    #[test]
    fn bootc_status_propagates_bad_json_as_error() {
        let dir = TempDir::new().unwrap();
        let runner = FakeCommandRunner::new().push(Ok("not json".to_string()));
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            runner,
        );

        assert!(ops.bootc_status().is_err());
    }

    #[test]
    fn bootc_status_propagates_command_failure_as_error() {
        let dir = TempDir::new().unwrap();
        let runner =
            FakeCommandRunner::new().push(Err(RunError::Failed("bootc: exit 1".to_string())));
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            runner,
        );

        assert!(ops.bootc_status().is_err());
    }

    // --- bootc_switch --------------------------------------------------

    #[test]
    fn bootc_switch_invokes_with_exact_image_ref() {
        let dir = TempDir::new().unwrap();
        let runner = FakeCommandRunner::new().push(Ok(String::new()));
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            dir.path().join("bk.json"),
            runner,
        );

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
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            &bk_path,
            FakeCommandRunner::new(),
        );
        let bk = Bookkeeping {
            desired_name: "v1".to_string(),
        };

        ops.write_bookkeeping(&bk).unwrap();

        assert_eq!(ops.read_bookkeeping().unwrap(), bk);
    }

    #[test]
    fn read_bookkeeping_missing_file_returns_default() {
        let dir = TempDir::new().unwrap();
        let bk_path = dir.path().join("does-not-exist.json");
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            &bk_path,
            FakeCommandRunner::new(),
        );

        assert_eq!(ops.read_bookkeeping().unwrap(), Bookkeeping::default());
    }

    #[test]
    fn bookkeeping_json_key_is_exactly_desired_name() {
        let dir = TempDir::new().unwrap();
        let bk_path = dir.path().join("bookkeeping.json");
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            &bk_path,
            FakeCommandRunner::new(),
        );

        ops.write_bookkeeping(&Bookkeeping {
            desired_name: "v1".to_string(),
        })
        .unwrap();

        // Parse as generic JSON (not our own struct) so a serde rename
        // mistake would actually be caught here instead of round-tripping
        // silently through matching (mis)configured field names.
        let raw = fs::read_to_string(&bk_path).unwrap();
        let value: serde_json::Value = serde_json::from_str(&raw).unwrap();
        assert_eq!(value["desiredName"], "v1");
        assert_eq!(value.as_object().unwrap().len(), 1);
    }

    #[test]
    fn bookkeeping_with_legacy_expected_digest_key_still_reads() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("bk.json");
        std::fs::write(
            &path,
            r#"{"expectedDigest":"sha256:OLD","desiredName":"legacy-name"}"#,
        )
        .unwrap();
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            &path,
            FakeCommandRunner::new(),
        );
        assert_eq!(
            ops.read_bookkeeping().unwrap(),
            Bookkeeping {
                desired_name: "legacy-name".to_string()
            }
        );
    }

    #[test]
    fn write_bookkeeping_never_leaves_a_half_written_file_visible() {
        // Not a concurrency test (this crate has no async story yet) —
        // just confirms the write goes through atomic_write's temp file
        // rather than a direct truncating write, by checking no stray
        // temp file is left behind afterwards.
        let dir = TempDir::new().unwrap();
        let bk_path = dir.path().join("bookkeeping.json");
        let mut ops = RealOps::with_runner(
            dir.path(),
            dir.path().join("archive"),
            &bk_path,
            FakeCommandRunner::new(),
        );

        ops.write_bookkeeping(&Bookkeeping::default()).unwrap();

        let entries: Vec<_> = fs::read_dir(dir.path()).unwrap().collect();
        assert_eq!(
            entries.len(),
            1,
            "expected only the final file, no leftover temp file"
        );
    }
}
