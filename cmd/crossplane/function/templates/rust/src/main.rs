//! The composition function's CLI entrypoint.

use clap::Parser;
use function_sdk_rust::{Args, logging, serve};

mod function;

#[tokio::main]
async fn main() -> Result<(), function_sdk_rust::Error> {
    let args = Args::parse();
    logging::configure(args.debug);
    serve(function::Function, &args).await
}
