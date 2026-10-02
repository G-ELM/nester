#!/usr/bin/env bash
# =============================================================================
# Mainnet fork fixture fetcher
#
# Downloads the real, currently-deployed WASM bytecode for the Blend lending
# pool and Soroswap pair that the adapter_lending/adapter_pool contracts are
# meant to integrate with, so the mainnet-fork tests (see each adapter's
# `mainnet_fork_test.rs`) can register the genuine contracts in a local
# Soroban test `Env` instead of this repo's simplified mocks
# (libs/test_utils/src/mocks/{lending,pool}.rs).
#
# This only fetches bytecode, not live ledger state — the fork tests
# initialize a fresh instance of the real contract rather than replaying its
# actual mainnet storage. That's enough to catch ABI/behavior drift between
# the mocks and the genuine protocol, which is what these tests exist for.
#
# Usage:
#   export BLEND_POOL_CONTRACT_ID=C...        # a real Blend pool on mainnet
#   export SOROSWAP_PAIR_CONTRACT_ID=C...      # a real Soroswap pair on mainnet
#   bash scripts/fetch-mainnet-fork.sh
#
# Then run the fork tests themselves (they no-op without this env var, so
# no --ignored flag is needed):
#   NESTER_MAINNET_FORK=1 cargo test -p adapter-lending-contract -p adapter-pool-contract --lib
#
# Prerequisites:
#   cargo install stellar-cli --features opt
# =============================================================================
set -euo pipefail

RPC_URL="${MAINNET_RPC_URL:-https://mainnet.sorobanrpc.com}"
NETWORK_PASSPHRASE="${MAINNET_NETWORK_PASSPHRASE:-Public Global Stellar Network ; September 2015}"

OUT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/tests/integration/fixtures/mainnet"
mkdir -p "$OUT_DIR"

fetch_wasm() {
    local label="$1" contract_id="$2" out_file="$3"

    if [ -z "$contract_id" ]; then
        echo "skip: ${label} contract id not set — see this script's header" >&2
        return 0
    fi

    echo "fetching ${label} (${contract_id}) from ${RPC_URL}..."
    stellar contract fetch \
        --id "$contract_id" \
        --rpc-url "$RPC_URL" \
        --network-passphrase "$NETWORK_PASSPHRASE" \
        --out-file "$out_file"
    echo "  -> $out_file"
}

fetch_wasm "Blend pool" "${BLEND_POOL_CONTRACT_ID:-}" "$OUT_DIR/blend_pool.wasm"
fetch_wasm "Soroswap pair" "${SOROSWAP_PAIR_CONTRACT_ID:-}" "$OUT_DIR/soroswap_pair.wasm"

echo "done. fixtures written to $OUT_DIR"
