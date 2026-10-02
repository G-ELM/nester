//! Mainnet fork test: adapter_lending against the real, deployed Blend pool
//! bytecode — not `nester_test_utils::mocks::MockLendingProtocol`.
//!
//! `MockLendingProtocol` is a deliberately simplified stand-in exposing
//! `deposit(from, amount)` / `withdraw(owner, to, units)` / `position_value`
//! / `supply_rate` (see its module doc). The real Blend pool's interface is
//! shaped very differently: value moves through `submit()`/
//! `submit_with_allowance()` with a list of `Request { request_type, address,
//! amount }` entries (SupplyCollateral/WithdrawCollateral/Borrow/Repay/…),
//! and positions are read via `get_positions(address) -> Positions`
//! (docs.blend.capital/tech-docs/integrations/integrate-pool). That is a
//! structural mismatch, not just different parameter values — adapter_lending
//! as written cannot drive a real Blend pool.
//!
//! A real Blend pool also can't be meaningfully initialized standalone: on
//! mainnet it's deployed and configured through `PoolFactoryContract::
//! deploy_pool`, wired to a specific oracle, backstop, and reserve list.
//! Reproducing that whole stack is out of scope here; this test is
//! deliberately limited to what's safe to assert without it — that the real
//! bytecode fetched by `scripts/fetch-mainnet-fork.sh` is valid and loads —
//! and documents the above as the concrete pre-mainnet blocker: fully
//! exercising this fork requires either fetching+deploying the real
//! PoolFactory/Oracle/Backstop contracts, or rewriting adapter_lending to
//! speak Blend's `submit`/`Request` interface (ideally against the official
//! `blend-contract-sdk` client rather than hand-rolled XDR).
//!
//! Disabled by default — see `mainnet_fork_enabled`. Run with:
//! `NESTER_MAINNET_FORK=1 cargo test -p adapter-lending-contract --lib`
//! after fetching the fixture.

#![cfg(test)]

extern crate std;

use soroban_sdk::Env;

use nester_test_utils::mainnet_fork::{mainnet_fork_enabled, register_real_contract, skip_reason};

#[test]
fn real_blend_pool_wasm_loads_cleanly() {
    if !mainnet_fork_enabled() {
        std::eprintln!("skipping mainnet fork test: {}", skip_reason());
        return;
    }

    let env = Env::default();

    // Registering the fetched bytecode is itself a meaningful check: it
    // fails loudly if the fixture is corrupt, truncated, or not actually a
    // Soroban contract (e.g. the fetch script was pointed at the wrong
    // contract id). Calling into it further requires the full
    // PoolFactory/Oracle/Backstop setup described above, which this test
    // does not attempt — see the module doc for why.
    let _pool_id = register_real_contract(&env, "blend_pool");
}
