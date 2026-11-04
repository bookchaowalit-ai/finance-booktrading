-- +migrate Up
-- The kill switch can only drain the current exchange, so each traded symbol
-- records the exchange it was traded on. '' = recorded before this column.
ALTER TABLE bot_traded_symbols ADD COLUMN IF NOT EXISTS exchange VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE bot_traded_symbols DROP CONSTRAINT IF EXISTS bot_traded_symbols_pkey;
ALTER TABLE bot_traded_symbols ADD PRIMARY KEY (symbol, exchange);
