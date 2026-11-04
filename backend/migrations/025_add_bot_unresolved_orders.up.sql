-- +migrate Up
-- Go bot submissions whose outcome is not known yet. Each is written before
-- the order is sent and deleted once the exchange's answer is known, so the
-- kill switch still sees an order that may go live after a backend restart.
CREATE TABLE IF NOT EXISTS bot_unresolved_orders (
    client_order_id VARCHAR(64) PRIMARY KEY,
    symbol          VARCHAR(32) NOT NULL,
    exchange        VARCHAR(32) NOT NULL DEFAULT '',
    submitted_at    TIMESTAMPTZ NOT NULL
);
