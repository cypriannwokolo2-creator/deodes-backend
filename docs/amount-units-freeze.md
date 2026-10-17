# Amount Units Freeze (Phase 0.2) — canonical rule, binding on all later phases

## Canonical rule

- **Arc EVM is the only chain.** All on-chain value is `uint256` **wei** (18 decimals). `pkg/arc.USDCDecimals = 18` is native/gas precision (`pkg/arc/chain.go:25`).
- The USDC **ERC-20 interface** at `0x3600...0000` exposes **6 decimals** (`pkg/arc.ERC20Decimals = 6`). Native (18) vs interface (6) confusion moves **1e12x** the intended value (`pkg/arc/chain.go:29-30`).
- `pkg/money` (`Precision = 7`, minor units) is **legacy** (pre-Arc 7-decimal). It MUST NOT feed EVM `uint256` values directly: 7-decimal minor units vs 18-decimal wei differ by **1e11x**.
- DB `NUMERIC(18,7)` columns (`002_create_circles.contribution_amount`, `004_create_contributions.amount`, `005_create_payouts.amount/fee_amount`, `006_create_penalties.amount`, `022_enterprise_security.*`, `031_create_disputes_votes_bids.bid_amount`, `038/040 deposits/withdrawals`, `046_treasury_fees.amount`, `052_subscriptions.amount/cap_total/collected_total`) are legacy precisions. Full migration to 18-decimal storage is Phase 2 scope; until then:
  1. Every DB↔chain conversion MUST go through `pkg/arc` helpers (`ERC20ToWei` / `WeiToERC20Units`) — never raw multiplication.
  2. `money.Money` values MUST be converted to decimal-string whole tokens first, then to wei — never `minor units → wei` by scaling the int64.
  3. All new migrations storing Arc value MUST use `NUMERIC(38,0)` (uint256-exact) or documented wei columns, never `(18,7)`.

## Helpers

- `pkg/arc.WeiPerToken = 10^18`, `pkg/arc.ERC20PerToken = 10^6`, `pkg/arc.WeiPerERC20Unit = 10^12`.
- `ERC20ToWei(u int64) (uint256 *big.Int, overflow bool)` and `WeiToERC20Units(w *big.Int) int64` (truncating, with negativity guard) are the only approved converters. They fail visibly instead of silently scaling wrong.
