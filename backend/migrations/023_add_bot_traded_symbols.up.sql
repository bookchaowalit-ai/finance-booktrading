-- +migrate Up
-- Every symbol the Go bot has traded. The kill switch drains open orders by
-- symbol, so this list must survive a backend restart.
CREATE TABLE IF NOT EXISTS bot_traded_symbols (
    symbol        VARCHAR(32) PRIMARY KEY,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
