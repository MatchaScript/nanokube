//! Compiles `../contract/desired.proto` into Rust gRPC bindings at build
//! time. The Go side already has generated bindings under
//! `contract/desiredpb`; this is the equivalent for Rust.

fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_prost_build::configure()
        .build_server(true)
        .build_client(false)
        .compile_protos(&["../contract/desired.proto"], &["../contract"])?;
    Ok(())
}
