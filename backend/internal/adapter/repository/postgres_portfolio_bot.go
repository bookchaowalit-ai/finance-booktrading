package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/domain/repository"
)

// PostgresPortfolioRepository implements PortfolioRepository with PostgreSQL
type PostgresPortfolioRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresPortfolioRepository creates a new PostgreSQL portfolio repository
func NewPostgresPortfolioRepository(pool *pgxpool.Pool) repository.PortfolioRepository {
	return &PostgresPortfolioRepository{
		pool: pool,
	}
}

// Get retrieves a portfolio by symbol
func (r *PostgresPortfolioRepository) Get(ctx context.Context, symbol model.TradeSymbol) (*model.Portfolio, error) {
	query := `
		SELECT symbol, balance, locked, avg_buy_price, updated_at
		FROM portfolio WHERE symbol = $1
	`
	portfolio := &model.Portfolio{}
	err := r.pool.QueryRow(ctx, query, symbol).Scan(
		&portfolio.Symbol,
		&portfolio.Balance,
		&portfolio.Locked,
		&portfolio.AvgBuyPrice,
		&portfolio.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return portfolio, nil
}

// Update updates a portfolio
func (r *PostgresPortfolioRepository) Update(ctx context.Context, portfolio *model.Portfolio) error {
	query := `
		INSERT INTO portfolio (symbol, balance, locked, avg_buy_price, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (symbol) DO UPDATE SET
			balance = EXCLUDED.balance,
			locked = EXCLUDED.locked,
			avg_buy_price = EXCLUDED.avg_buy_price,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.pool.Exec(ctx, query,
		portfolio.Symbol,
		portfolio.Balance,
		portfolio.Locked,
		portfolio.AvgBuyPrice,
		time.Now(),
	)
	return err
}

// GetAll retrieves all portfolios
func (r *PostgresPortfolioRepository) GetAll(ctx context.Context) ([]*model.Portfolio, error) {
	query := `
		SELECT symbol, balance, locked, avg_buy_price, updated_at
		FROM portfolio ORDER BY symbol
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var portfolios []*model.Portfolio
	for rows.Next() {
		portfolio := &model.Portfolio{}
		err := rows.Scan(
			&portfolio.Symbol,
			&portfolio.Balance,
			&portfolio.Locked,
			&portfolio.AvgBuyPrice,
			&portfolio.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		portfolios = append(portfolios, portfolio)
	}
	return portfolios, rows.Err()
}

// PostgresBotStatusRepository implements BotStatusRepository with PostgreSQL
type PostgresBotStatusRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresBotStatusRepository creates a new PostgreSQL bot status repository
func NewPostgresBotStatusRepository(pool *pgxpool.Pool) repository.BotStatusRepository {
	return &PostgresBotStatusRepository{
		pool: pool,
	}
}

// Get retrieves the current bot status
func (r *PostgresBotStatusRepository) Get(ctx context.Context) (*model.BotStatus, error) {
	query := `
		SELECT is_active, total_trades, total_profit
		FROM bot_status ORDER BY id DESC LIMIT 1
	`
	status := &model.BotStatus{}

	err := r.pool.QueryRow(ctx, query).Scan(
		&status.IsActive,
		&status.TotalTrades,
		&status.TotalProfit,
	)
	if err != nil {
		// If there's no bot_status record, return a default one
		if err == pgx.ErrNoRows {
			return &model.BotStatus{
				IsActive:    false,
				TotalTrades: 0,
				TotalProfit: 0,
			}, nil
		}
		return nil, err
	}

	return status, nil
}

// SetActive sets the bot active status
func (r *PostgresBotStatusRepository) SetActive(ctx context.Context, active bool) error {
	query := `
		UPDATE bot_status SET is_active = $1, updated_at = $2
		WHERE id = (SELECT id FROM bot_status ORDER BY id DESC LIMIT 1)
	`
	_, err := r.pool.Exec(ctx, query, active, time.Now())
	return err
}

// IncrementTrades increments the total trades count
func (r *PostgresBotStatusRepository) IncrementTrades(ctx context.Context) error {
	query := `
		UPDATE bot_status SET total_trades = total_trades + 1, updated_at = $1
		WHERE id = (SELECT id FROM bot_status ORDER BY id DESC LIMIT 1)
	`
	_, err := r.pool.Exec(ctx, query, time.Now())
	return err
}

// UpdateProfit updates the total profit
func (r *PostgresBotStatusRepository) UpdateProfit(ctx context.Context, profit float64) error {
	query := `
		UPDATE bot_status SET total_profit = total_profit + $1, updated_at = $2
		WHERE id = (SELECT id FROM bot_status ORDER BY id DESC LIMIT 1)
	`
	_, err := r.pool.Exec(ctx, query, profit, time.Now())
	return err
}

// AddTradedSymbol records a symbol the bot traded (idempotent).
func (r *PostgresBotStatusRepository) AddTradedSymbol(ctx context.Context, symbol, exchange string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO bot_traded_symbols (symbol, exchange) VALUES ($1, $2)
		ON CONFLICT (symbol, exchange) DO NOTHING
	`, symbol, exchange)
	return err
}

// ListTradedSymbols returns every symbol the bot has traded, per exchange.
func (r *PostgresBotStatusRepository) ListTradedSymbols(ctx context.Context) ([]model.TradedSymbol, error) {
	rows, err := r.pool.Query(ctx, `SELECT symbol, exchange FROM bot_traded_symbols ORDER BY symbol, exchange`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TradedSymbol
	for rows.Next() {
		var ts model.TradedSymbol
		if err := rows.Scan(&ts.Symbol, &ts.Exchange); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// PostgresMarketDataRepository implements MarketDataRepository with PostgreSQL + TimescaleDB
type PostgresMarketDataRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresMarketDataRepository creates a new PostgreSQL market data repository
func NewPostgresMarketDataRepository(pool *pgxpool.Pool) repository.MarketDataRepository {
	return &PostgresMarketDataRepository{
		pool: pool,
	}
}

// GetLatest retrieves the latest market data for a symbol
func (r *PostgresMarketDataRepository) GetLatest(ctx context.Context, symbol model.TradeSymbol) (*model.MarketData, error) {
	query := `
		SELECT time, symbol, price, volume
		FROM market_data WHERE symbol = $1
		ORDER BY time DESC LIMIT 1
	`
	data := &model.MarketData{}
	err := r.pool.QueryRow(ctx, query, symbol).Scan(
		&data.Timestamp,
		&data.Symbol,
		&data.Price,
		&data.Volume,
	)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Save saves market data
func (r *PostgresMarketDataRepository) Save(ctx context.Context, data *model.MarketData) error {
	query := `
		INSERT INTO market_data (time, symbol, price, volume)
		VALUES ($1, $2, $3, $4)
	`
	_, err := r.pool.Exec(ctx, query, data.Timestamp, data.Symbol, data.Price, data.Volume)
	return err
}

// GetPriceHistory retrieves price history for a duration
func (r *PostgresMarketDataRepository) GetPriceHistory(ctx context.Context, symbol model.TradeSymbol, duration time.Duration) ([]*model.MarketData, error) {
	query := `
		SELECT time, symbol, price, volume
		FROM market_data
		WHERE symbol = $1 AND time >= $2
		ORDER BY time DESC
	`
	rows, err := r.pool.Query(ctx, query, symbol, time.Now().Add(-duration))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.MarketData
	for rows.Next() {
		data := &model.MarketData{}
		err := rows.Scan(
			&data.Timestamp,
			&data.Symbol,
			&data.Price,
			&data.Volume,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, data)
	}
	return result, rows.Err()
}

var _ repository.TradedSymbolStore = (*PostgresBotStatusRepository)(nil)
