//! nanokube-agent core library. `main.rs` is a thin binary entrypoint;
//! the apply pipeline itself lives here so it stays testable and, later,
//! usable from a real CLI/gRPC entrypoint without restructuring.

pub mod apply_once;
pub mod desiredpb;
pub mod ops;
pub mod pipeline;
pub mod server;
