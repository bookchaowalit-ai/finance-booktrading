// rekey-dex-wallets moves dex_wallets.private_key_encrypted from the legacy
// key that was hard-coded in wallet_service.go to DEX_WALLET_ENCRYPTION_KEY.
//
// Usage (dry run, changes nothing):
//
//	DATABASE_URL=... DEX_WALLET_ENCRYPTION_KEY=... go run ./cmd/rekey-dex-wallets
//
// Add -apply to rewrite the legacy rows in one transaction. Take a database
// backup first. Rows that open with neither key are reported and left alone.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"

	"trading-bot-system/backend/internal/domain/service"
)

func main() {
	apply := flag.Bool("apply", false, "rewrite legacy rows (default: report only)")
	flag.Parse()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	if err := service.ValidateDexWalletKey(); err != nil {
		slog.Error("wallet key", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		slog.Error("connect", "error", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		slog.Error("begin", "error", err)
		os.Exit(1)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT id, address, private_key_encrypted FROM dex_wallets FOR UPDATE`)
	if err != nil {
		slog.Error("select", "error", err)
		os.Exit(1)
	}
	type rekey struct{ id, address, ciphertext string }
	var legacy []rekey
	var current, unknown int
	for rows.Next() {
		var id, address, enc string
		if err := rows.Scan(&id, &address, &enc); err != nil {
			slog.Error("scan", "error", err)
			os.Exit(1)
		}
		if next, err := service.ReencryptLegacyWalletCiphertext(enc); err == nil {
			legacy = append(legacy, rekey{id, address, next})
			continue
		}
		if service.OpensWithDexWalletKey(enc) {
			current++
			continue
		}
		unknown++
		slog.Warn("wallet opens with neither key", "id", id, "address", address)
	}
	if err := rows.Err(); err != nil {
		slog.Error("rows", "error", err)
		os.Exit(1)
	}

	for _, r := range legacy {
		slog.Info("legacy wallet", "id", r.id, "address", r.address)
	}
	slog.Info("summary", "legacy", len(legacy), "already_rekeyed", current, "unknown", unknown, "apply", *apply)
	if !*apply || len(legacy) == 0 {
		return
	}

	for _, r := range legacy {
		if _, err := tx.Exec(ctx,
			`UPDATE dex_wallets SET private_key_encrypted = $1, updated_at = NOW() WHERE id = $2`,
			r.ciphertext, r.id); err != nil {
			slog.Error("update", "id", r.id, "error", err)
			os.Exit(1)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("commit", "error", err)
		os.Exit(1)
	}
	slog.Info("rekeyed", "rows", len(legacy))
}
