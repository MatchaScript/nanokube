//! nanokube-agent binary: two entry points into one `apply` pipeline
//! (see `nanokube_agent::pipeline`).
//!
//! - `serve` (the default when no subcommand is given): a dev-grade
//!   (plaintext) tonic gRPC server exposing `Agent::PushDesired`.
//! - `apply --once --desired <name>.json`: applies a single desired
//!   document read from local files, then exits.

use std::net::SocketAddr;
use std::path::PathBuf;
use std::process::ExitCode;

use clap::{Args, Parser, Subcommand};

use nanokube_agent::apply_once::apply_once;
use nanokube_agent::desiredpb::agent_server::AgentServer;
use nanokube_agent::ops::RealOps;
use nanokube_agent::server::{AgentService, MAX_DESIRED_MESSAGE_BYTES, RealOpsProvider};

/// Production defaults, matching the architecture doc's stated paths
/// (`docs/nanokube/2026-07-06-nanokube-component-architecture-rev5.md`).
const DEFAULT_LISTEN: &str = "0.0.0.0:9090";
const DEFAULT_CONFEXTS_DIR: &str = "/var/lib/confexts";
const DEFAULT_ARCHIVE_DIR: &str = "/var/lib/nanokube/confexts-archive";
const DEFAULT_BOOKKEEPING_PATH: &str = "/var/lib/nanokube/state/agent-bookkeeping.json";

#[derive(Parser)]
#[command(
    name = "nanokube-agent",
    version,
    about = "nanokube node agent: applies desired documents (confext DDI)"
)]
struct Cli {
    #[command(subcommand)]
    command: Option<Command>,
}

#[derive(Subcommand)]
enum Command {
    /// Start the gRPC server (also the default when no subcommand is
    /// given).
    Serve(ServeArgs),
    /// Apply a single desired document read from local files, then
    /// exit.
    Apply(ApplyArgs),
}

#[derive(Args)]
struct ServeArgs {
    /// Address to listen on. Step 1 is dev-grade: plaintext, no TLS.
    #[arg(long, default_value = DEFAULT_LISTEN)]
    listen: String,
    #[arg(long, default_value = DEFAULT_CONFEXTS_DIR)]
    confexts_dir: PathBuf,
    #[arg(long, default_value = DEFAULT_ARCHIVE_DIR)]
    archive_dir: PathBuf,
    #[arg(long, default_value = DEFAULT_BOOKKEEPING_PATH)]
    bookkeeping_path: PathBuf,
}

impl Default for ServeArgs {
    fn default() -> Self {
        ServeArgs {
            listen: DEFAULT_LISTEN.to_string(),
            confexts_dir: PathBuf::from(DEFAULT_CONFEXTS_DIR),
            archive_dir: PathBuf::from(DEFAULT_ARCHIVE_DIR),
            bookkeeping_path: PathBuf::from(DEFAULT_BOOKKEEPING_PATH),
        }
    }
}

#[derive(Args)]
struct ApplyArgs {
    /// Apply once and exit. Currently the only supported mode — the
    /// flag exists because the plan names it explicitly
    /// (`apply --once --desired <name>.json`).
    #[arg(long)]
    once: bool,
    /// Path to the `<name>.json` DesiredMetadata sidecar. Its sibling
    /// `<name>.raw` (same directory, same basename) is read as the blob.
    #[arg(long)]
    desired: PathBuf,
    #[arg(long, default_value = DEFAULT_CONFEXTS_DIR)]
    confexts_dir: PathBuf,
    #[arg(long, default_value = DEFAULT_ARCHIVE_DIR)]
    archive_dir: PathBuf,
    #[arg(long, default_value = DEFAULT_BOOKKEEPING_PATH)]
    bookkeeping_path: PathBuf,
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    match cli.command {
        None => serve(ServeArgs::default()),
        Some(Command::Serve(args)) => serve(args),
        Some(Command::Apply(args)) => apply_cmd(args),
    }
}

fn serve(args: ServeArgs) -> ExitCode {
    let addr: SocketAddr = match args.listen.parse() {
        Ok(addr) => addr,
        Err(e) => {
            eprintln!("invalid --listen address {:?}: {e}", args.listen);
            return ExitCode::FAILURE;
        }
    };

    let rt = match tokio::runtime::Runtime::new() {
        Ok(rt) => rt,
        Err(e) => {
            eprintln!("failed to start async runtime: {e}");
            return ExitCode::FAILURE;
        }
    };

    let provider = RealOpsProvider::new(args.confexts_dir, args.archive_dir, args.bookkeeping_path);
    let service = AgentService::new(provider);

    rt.block_on(async move {
        println!("nanokube-agent: listening on {addr} (plaintext, dev-grade)");
        match tonic::transport::Server::builder()
            .add_service(
                AgentServer::new(service).max_decoding_message_size(MAX_DESIRED_MESSAGE_BYTES),
            )
            .serve(addr)
            .await
        {
            Ok(()) => ExitCode::SUCCESS,
            Err(e) => {
                eprintln!("server error: {e}");
                ExitCode::FAILURE
            }
        }
    })
}

fn apply_cmd(args: ApplyArgs) -> ExitCode {
    if !args.once {
        eprintln!("apply: --once is required (no other apply mode is implemented)");
        return ExitCode::FAILURE;
    }

    let mut ops = RealOps::new(args.confexts_dir, args.archive_dir, args.bookkeeping_path);
    match apply_once(&args.desired, &mut ops) {
        Ok(name) => {
            println!("applied {name}");
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("apply failed: {e}");
            ExitCode::FAILURE
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn version_is_set() {
        assert!(!env!("CARGO_PKG_VERSION").is_empty());
    }
}
