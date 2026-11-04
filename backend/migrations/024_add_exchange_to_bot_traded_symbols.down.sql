-- +migrate Down
DELETE FROM bot_traded_symbols a USING bot_traded_symbols b
 WHERE a.symbol = b.symbol AND a.exchange > b.exchange;
ALTER TABLE bot_traded_symbols DROP CONSTRAINT IF EXISTS bot_traded_symbols_pkey;
ALTER TABLE bot_traded_symbols DROP COLUMN IF EXISTS exchange;
ALTER TABLE bot_traded_symbols ADD PRIMARY KEY (symbol);
