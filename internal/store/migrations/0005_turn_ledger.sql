-- 0005_turn_ledger: the cost of every model turn, beside the decisions ledger that already exists.
--
-- Spend is recorded, not shown (PLAN.md 6, docs/slices/S08-agent-pipeline.md 5). A row holds ids, counts and money,
-- never text: the ledger must not become a second copy of a review. decision_calls already stores the pre-read;
-- this table stores the chat turn.
CREATE TABLE turn_calls (
    id                TEXT PRIMARY KEY,
    session_id        TEXT REFERENCES sessions (id) ON DELETE RESTRICT,
    turn_index        INTEGER NOT NULL DEFAULT 0,
    model             TEXT NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL NOT NULL DEFAULT 0,
    latency_ms        INTEGER NOT NULL DEFAULT 0,
    reason            TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL
);

CREATE INDEX turn_calls_session ON turn_calls (session_id, turn_index);
