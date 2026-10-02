//! Mainnet fork test: adapter_pool against the real, deployed SoroswapPair
//! bytecode — not `nester_test_utils::mocks::MockAmmPool`.
//!
//! `MockAmmPool` is a deliberately simplified single-sided stand-in (see its
//! module doc). This test loads the genuine SoroswapPair WASM (fetched by
//! `scripts/fetch-mainnet-fork.sh`) into the test `Env` and drives it through
//! its own real interface, so any drift between the mock's assumptions and
//! the live protocol surfaces here instead of after a mainnet deposit.
//!
//! Disabled by default — see `mainnet_fork_enabled`. Run with:
//! `NESTER_MAINNET_FORK=1 cargo test -p adapter-pool-contract --lib`
//! after fetching the fixture.

#![cfg(test)]

extern crate std;

use soroban_sdk::{
    contractclient, testutils::Address as _, token::StellarAssetClient, Address, Env,
};

use nester_test_utils::mainnet_fork::{mainnet_fork_enabled, register_real_contract, skip_reason};

use crate::{PoolAdapterContract, PoolAdapterContractClient};

/// Client for the real SoroswapPair contract, scoped to the functions this
/// test needs. Signatures are taken from Soroswap's own technical reference
/// (docs.soroswap.finance/.../smart-contracts/soroswappair) rather than from
/// `MockAmmPool` — the whole point of this test is to catch where those two
/// diverge, so it must not borrow the mock's (possibly wrong) assumptions.
#[contractclient(name = "SoroswapPairClient")]
trait SoroswapPair {
    fn initialize(e: Env, factory: Address, token_0: Address, token_1: Address);
    /// Low-level mint: caller must already have transferred both token_0 and
    /// token_1 into the pair's own balance before calling this. Unlike
    /// `MockAmmPool::deposit`, there is no `amount` argument — the minted
    /// amount is derived from the balance delta the pair observes itself.
    fn deposit(e: Env, to: Address) -> i128;
    fn get_reserves(e: Env) -> (i128, i128);
}

#[test]
fn real_soroswap_pair_diverges_from_mock_amm_pool_interface() {
    if !mainnet_fork_enabled() {
        std::eprintln!("skipping mainnet fork test: {}", skip_reason());
        return;
    }

    let env = Env::default();
    env.mock_all_auths();

    let vault = Address::generate(&env);
    // SoroswapPair::initialize takes a factory address but get_reserves/
    // deposit (per the signatures above) never call back into it, so an
    // undeployed placeholder address is sufficient for this test.
    let factory = Address::generate(&env);

    let token_0_admin = Address::generate(&env);
    let token_1_admin = Address::generate(&env);
    let token_0_id = env
        .register_stellar_asset_contract_v2(token_0_admin)
        .address();
    let token_1_id = env
        .register_stellar_asset_contract_v2(token_1_admin)
        .address();
    let token_0_admin_client = StellarAssetClient::new(&env, &token_0_id);
    let token_1_admin_client = StellarAssetClient::new(&env, &token_1_id);

    let pair_id = register_real_contract(&env, "soroswap_pair");
    let pair = SoroswapPairClient::new(&env, &pair_id);
    pair.initialize(&factory, &token_0_id, &token_1_id);

    // Ground truth from the real contract: a freshly initialized pair holds
    // no reserves.
    assert_eq!(pair.get_reserves(), (0, 0));

    // Fund both sides directly (the push model deposit() expects) and mint
    // through the pair's own real logic — not the adapter.
    const SEED: i128 = 10_000_000;
    token_0_admin_client.mint(&pair_id, &SEED);
    token_1_admin_client.mint(&pair_id, &SEED);
    let minted = pair.deposit(&vault);
    assert!(
        minted > 0,
        "real SoroswapPair should mint LP units once both reserves are funded"
    );
    assert_eq!(pair.get_reserves(), (SEED, SEED));

    // Deliberately NOT exercised: calling adapter_pool's own deposit()
    // against `pair_id`. adapter_pool.deposit(from, amount, min_units_out)
    // sends a single underlying asset and expects the pool to compute units
    // from `amount` (see contracts/adapter_pool/src/lib.rs); the real pair's
    // deposit(to) takes no amount at all and requires BOTH token_0 and
    // token_1 to already be funded. Those two call shapes are incompatible
    // — invoking the adapter against a real pair as adapter_pool stands
    // today cannot work. That mismatch, not a specific panic string, is
    // this test's finding: adapter_pool must gain either a router-mediated
    // single-asset path or genuinely dual-asset deposit/withdraw before it
    // can be pointed at a real Soroswap pair.
    let adapter_id = env.register_contract(None, PoolAdapterContract);
    let adapter = PoolAdapterContractClient::new(&env, &adapter_id);
    adapter.initialize(&vault, &pair_id, &token_0_id);
    assert_eq!(adapter.get_pool(), pair_id);
}
