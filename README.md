# Blacklist Watcher (EXPERIMENTAL)

Monitors blacklist-related events for stablecoin contracts:
- ⬡ Ethereum: USDT, USDC (and the USDT multisig submissions)
- 🟥 Tron (TRC20): USDT (including multisig submissions when configured)
- 🅿️ Polkadot Asset Hub: USDT, USDC (account freezes / thaws)

⚠️ This is just proof of concept.

live version [bl.dzen.ws](https://bl.dzen.ws/)

## Environment

- `ETH_NODE_URL` – Ethereum WebSocket endpoint
- `TRON_NODE_URL` – Tron HTTP endpoint (TronGrid is HTTPS-only, e.g. `https://api.trongrid.io`)
- `TRON_API_KEY` – optional Trongrid API key (if your endpoint requires it)
- `TRON_USDT_CONTRACT` – optional override for the TRC20 USDT contract (hex or base58)
- `TRON_MULTISIG_CONTRACT` – optional TRON multisig address for submission events (hex or base58)
- `TRON_START_BLOCK` – optional start block for Tron scanning (defaults to latest when absent)
- `POLKADOT_NODE_URL` – Polkadot Asset Hub WebSocket endpoint
- `POLKADOT_USDT_ASSET_ID` / `POLKADOT_USDC_ASSET_ID` – asset IDs to watch on Asset Hub (required when enabling Polkadot watcher)
- `POLKADOT_START_BLOCK` – optional start block for Polkadot scanning (defaults to latest when absent)
- `POLKADOT_SS58_PREFIX` – optional SS58 prefix for address formatting (defaults to 0 for Polkadot)
