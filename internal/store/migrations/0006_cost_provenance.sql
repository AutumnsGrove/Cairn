-- 0006_cost_provenance: whose dollars a call spent, beside how much it spent.
--
-- A call reports what it consumed (cost_usd) and where that number came from: the provider's own report, a plan
-- allowance, or a catalog estimate. An allowance-covered call is not money spent, so the covered part is stored
-- beside the total instead of being guessed from it later (nova's provider.Usage, docs/BUILD-LOG.md 2026-11-07).
--
-- cost_source has no CHECK on purpose. It is an external system's vocabulary, not Sepia's: a nova that learns a new
-- word must not make the receipt fail to write, because a ledger row that cannot be written loses a fact.
--
-- Append-only and positional like every migration (docs/slices/S04-data-layer.md 1).

ALTER TABLE decision_calls ADD COLUMN cost_source TEXT NOT NULL DEFAULT '';
ALTER TABLE decision_calls ADD COLUMN allowance_usd REAL NOT NULL DEFAULT 0;

ALTER TABLE turn_calls ADD COLUMN cost_source TEXT NOT NULL DEFAULT '';
ALTER TABLE turn_calls ADD COLUMN allowance_usd REAL NOT NULL DEFAULT 0;
