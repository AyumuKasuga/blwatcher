# Blacklist Watcher (EXPERIMENTAL)

Monitors blacklist-related events for stablecoin contracts:
- ⬡ Ethereum: USDT, USDC (and the USDT multisig submissions)
- ⚪ Arbitrum: USDC
- 🟦 Base: USDC
- 🟥 Tron (TRC20): USDT (including multisig submissions when configured)

⚠️ This is just proof of concept.

live version [bl.dzen.ws](https://bl.dzen.ws/)

## Environment

- `ETH_NODE_URL` – Ethereum WebSocket endpoint
- `ARBITRUM_NODE_URL` – Arbitrum WebSocket endpoint (USDC)
- `BASE_NODE_URL` – Base WebSocket endpoint (USDC)
- `TRON_NODE_URL` – Tron HTTP endpoint (TronGrid is HTTPS-only, e.g. `https://api.trongrid.io`)
- `TRON_API_KEY` – optional Trongrid API key (if your endpoint requires it)
- `TRON_USDT_CONTRACT` – optional override for the TRC20 USDT contract (hex or base58)
- `TRON_MULTISIG_CONTRACT` – optional TRON multisig address for submission events (hex or base58)
- `TRON_START_BLOCK` – optional start block for Tron scanning (defaults to latest when absent)
