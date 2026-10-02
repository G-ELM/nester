// ---------------------------------------------------------------------------
// Mainnet fork testing support.
//
// The mocks in `mocks/` stand in for Blend and Soroswap with deliberately
// simplified, single-sided interfaces so adapter unit tests can run with no
// network access. That simplification is exactly the risk this module exists
// to catch: it loads the *real* Blend pool / Soroswap pair WASM — fetched
// from mainnet by `scripts/fetch-mainnet-fork.sh` — into the same in-process
// Soroban test `Env`, so adapter integration tests can run against the
// genuine deployed bytecode instead of a hand-written approximation of it.
//
// Fork tests are opt-in (see `mainnet_fork_enabled`) and skip cleanly when
// the fixtures haven't been fetched, so `cargo test`/`make test` never
// requires network access or mainnet credentials.
// ---------------------------------------------------------------------------

extern crate std;

use soroban_sdk::{Address, Env};
use std::{fs, path::PathBuf, string::String};

/// Env var that gates fork tests. Unset (the default) means "skip": fork
/// tests assert nothing and exit early rather than failing a `cargo test`
/// run that never fetched any fixtures.
pub const MAINNET_FORK_ENV_VAR: &str = "NESTER_MAINNET_FORK";

/// Whether fork tests should actually run. Checked explicitly (rather than
/// just "does the fixture file exist") so a fixture left over from a prior
/// run doesn't silently turn on mainnet assertions in an unrelated CI job.
pub fn mainnet_fork_enabled() -> bool {
    std::env::var(MAINNET_FORK_ENV_VAR).map(|v| v == "1").unwrap_or(false)
}

/// Directory fetch-mainnet-fork.sh writes fetched contract WASM into, and
/// where `load_wasm_fixture` reads it back from. Relative to this crate's
/// `CARGO_MANIFEST_DIR`, so it resolves correctly regardless of the test
/// binary's working directory.
fn fixtures_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../tests/integration/fixtures/mainnet")
}

/// Reads a previously-fetched contract WASM fixture by name (e.g.
/// `"blend_pool"`, `"soroswap_pair"` — no extension). Panics with a message
/// pointing at the fetch script if the file isn't there, rather than a bare
/// `No such file or directory` — a fork test failing this way almost always
/// means the fixture was never fetched, not a real bug.
pub fn load_wasm_fixture(name: &str) -> std::vec::Vec<u8> {
    let path = fixtures_dir().join(format!("{name}.wasm"));
    fs::read(&path).unwrap_or_else(|err| {
        panic!(
            "mainnet fork fixture {:?} not found ({err}); run \
             `scripts/fetch-mainnet-fork.sh` first (see that script's header \
             for the env vars it needs)",
            path
        )
    })
}

/// Loads a fetched WASM fixture into `env` and returns its contract address.
/// Thin wrapper over `Env::register_contract_wasm` — the real contract's own
/// `initialize`/constructor must still be invoked by the caller, exactly as
/// it would be against the genuine mainnet deployment.
pub fn register_real_contract(env: &Env, fixture_name: &str) -> Address {
    let wasm = load_wasm_fixture(fixture_name);
    env.register_contract_wasm(None, wasm.as_slice())
}

/// Human-readable reason a fork test is being skipped, for a consistent
/// `std::eprintln!`/panic message across the fork test module.
pub fn skip_reason() -> String {
    format!(
        "mainnet fork tests are disabled; set {MAINNET_FORK_ENV_VAR}=1 and run \
         scripts/fetch-mainnet-fork.sh to enable them"
    )
}
