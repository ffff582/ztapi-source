# Xcloud Primary Routing and Finance Estimate Design

## Goal

Use the existing Xcloud channel as the primary route only for models that are
both explicitly configured on that channel and present in the live Xcloud
public pricing catalogue. Keep Yunxin as the fallback route. Extend the
finance report with a clearly labelled Xcloud public-quote estimate without
turning a public retail quote into a confirmed supplier debit.

## Current facts

- Xcloud channel `4` exists but is disabled.
- The channel currently declares seven Claude source models.
- The live public Xcloud catalogue contains 23 Chat models and no Responses,
  image, or video endpoint. It contains public quote data for six of the seven
  configured Claude models; `claude-opus-5` is not present in the public quote
  list.
- Yunxin channels remain enabled and are the fallback for models not admitted
  to Xcloud.

## Routing

1. Enable channel 4 and give its admitted model abilities a higher priority
   than Yunxin, preserving the existing channel group and model mapping.
2. For each admitted published model, create a new immutable publication
   snapshot whose allowed channel list includes Xcloud and the existing Yunxin
   fallback. Never mutate an old publication snapshot in place.
3. Do not add new source models merely because they appear on the public page;
   the stored Xcloud credential and channel mapping must declare them first.
4. Leave `claude-opus-5` on the existing Yunxin route until a quote and a
   successful compatibility check are available.

## Finance

- Read settled request usage dimensions and final attempt channel from the
  existing immutable settlement records.
- Calculate an Xcloud public-quote estimate per model and channel using
  exclusive usage dimensions. `cache_write` is a total when TTL buckets are
  present; it must not be added again on top of `cache_write_5m` and
  `cache_write_1h`.
- Show estimated revenue, estimated Xcloud cost, and estimated gross profit
  separately from the confirmed actual supplier cost and confirmed gross
  profit. Missing usage, unknown model quote, or incomplete channel lineage
  marks the estimate incomplete.
- The existing manual supplier cost facts remain the only source for
  `ActualCost`, `GrossProfit`, and `Confirmed`.

## Verification and release

- Add unit tests for exclusive cache pricing and incomplete/unknown quote
  handling.
- Add routing tests proving Xcloud wins for an admitted model and Yunxin wins
  when Xcloud is disabled or absent from the publication snapshot.
- Run finance, middleware, and frontend build checks before deployment.
- Back up the affected channel, ability, model, and publication rows before
  applying the production route change. Verify the live route and finance JSON
  after deployment.

## Non-goals

- No automatic fallback across unsupported protocols.
- No assumption that the Xcloud public quote equals the supplier's real cash
  debit.
- No changes to customer balances, historical billing, or existing Yunxin
  cost facts.
