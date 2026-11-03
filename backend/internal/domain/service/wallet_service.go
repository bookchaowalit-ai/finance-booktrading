package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/hkdf"

	"trading-bot-system/backend/internal/config"
	"trading-bot-system/backend/internal/logger"
)

// Wallet represents a DEX wallet
type Wallet struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Address   string    `json:"address"`
	ChainID   int64     `json:"chain_id"`
	ChainName string    `json:"chain_name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WalletBalance represents wallet balance information
type WalletBalance struct {
	Address       string              `json:"address"`
	NativeBalance *big.Int            `json:"native_balance"`
	NativeSymbol  string              `json:"native_symbol"`
	TokenBalances map[string]*big.Int `json:"token_balances"`
	USDEquivalent float64             `json:"usd_equivalent"`
}

// WalletService manages DEX wallets (creation, import, signing, transactions)
type WalletService struct {
	mu      sync.RWMutex
	pool    *pgxpool.Pool
	cfg     *config.DexConfig
	client  *ethclient.Client
	chainID *big.Int
	// In-memory private key for active session (encrypted at rest)
	privateKey *ecdsa.PrivateKey
	address    common.Address
}

// NewWalletService creates a new wallet service
func NewWalletService(pool *pgxpool.Pool, cfg *config.DexConfig) *WalletService {
	return &WalletService{
		pool: pool,
		cfg:  cfg,
	}
}

// Initialize connects to the RPC and sets up the chain
func (s *WalletService) Initialize(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chainCfg, ok := s.cfg.Chains[s.cfg.DefaultChain]
	if !ok {
		return fmt.Errorf("chain configuration not found for chain ID %d", s.cfg.DefaultChain)
	}

	client, err := ethclient.DialContext(ctx, chainCfg.RPCURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RPC: %w", err)
	}

	s.client = client
	s.chainID = big.NewInt(int64(chainCfg.ChainID))

	logger.Info("DEX wallet service initialized",
		"chain", chainCfg.Name,
		"chain_id", chainCfg.ChainID,
		"rpc", chainCfg.RPCURL,
	)
	return nil
}

// CreateWallet generates a new wallet and returns the address
func (s *WalletService) CreateWallet(ctx context.Context, userID string) (*Wallet, error) {
	// Generate new private key
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	publicKeyHex := hex.EncodeToString(crypto.FromECDSAPub(&privateKey.PublicKey))

	// Encrypt private key for storage
	encryptedKey, err := s.encryptPrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt private key: %w", err)
	}

	// Store in database
	_, err = s.pool.Exec(ctx, `
		INSERT INTO dex_wallets (id, user_id, address, chain_id, private_key_encrypted, public_key, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NOW(), NOW())
	`, userID, address.Hex(), s.chainID.Int64(), encryptedKey, publicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to store wallet: %w", err)
	}

	wallet := &Wallet{
		UserID:    userID,
		Address:   address.Hex(),
		ChainID:   s.chainID.Int64(),
		ChainName: s.getChainName(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	logger.Info("DEX wallet created", "user_id", userID, "address", address.Hex())
	return wallet, nil
}

// ImportWallet imports an existing wallet by private key
func (s *WalletService) ImportWallet(ctx context.Context, userID, privateKeyHex string) (*Wallet, error) {
	// Parse private key
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	publicKeyHex := hex.EncodeToString(crypto.FromECDSAPub(&privateKey.PublicKey))

	// Check if wallet already exists
	var exists bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM dex_wallets WHERE address = $1)
	`, address.Hex()).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to check wallet existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("wallet already exists for address %s", address.Hex())
	}

	// Encrypt private key for storage
	encryptedKey, err := s.encryptPrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt private key: %w", err)
	}

	// Store in database
	_, err = s.pool.Exec(ctx, `
		INSERT INTO dex_wallets (id, user_id, address, chain_id, private_key_encrypted, public_key, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NOW(), NOW())
	`, userID, address.Hex(), s.chainID.Int64(), encryptedKey, publicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to store wallet: %w", err)
	}

	wallet := &Wallet{
		UserID:    userID,
		Address:   address.Hex(),
		ChainID:   s.chainID.Int64(),
		ChainName: s.getChainName(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	logger.Info("DEX wallet imported", "user_id", userID, "address", address.Hex())
	return wallet, nil
}

// LoadWallet loads a wallet and decrypts the private key for signing
func (s *WalletService) LoadWallet(ctx context.Context, userID, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var encryptedKey, publicKeyHex string
	err := s.pool.QueryRow(ctx, `
		SELECT private_key_encrypted, public_key FROM dex_wallets
		WHERE user_id = $1 AND address = $2
	`, userID, address).Scan(&encryptedKey, &publicKeyHex)
	if err != nil {
		return fmt.Errorf("wallet not found: %w", err)
	}

	// Decrypt private key
	privateKey, err := s.decryptPrivateKey(encryptedKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt private key: %w", err)
	}

	s.privateKey = privateKey
	s.address = crypto.PubkeyToAddress(privateKey.PublicKey)

	logger.Info("DEX wallet loaded for signing", "address", s.address.Hex())
	return nil
}

// LoadWalletById loads a wallet by its database ID
func (s *WalletService) LoadWalletById(ctx context.Context, userID, walletID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var encryptedKey, publicKeyHex string
	err := s.pool.QueryRow(ctx, `
		SELECT private_key_encrypted, public_key FROM dex_wallets
		WHERE id = $1 AND user_id = $2
	`, walletID, userID).Scan(&encryptedKey, &publicKeyHex)
	if err != nil {
		return fmt.Errorf("wallet not found: %w", err)
	}

	privateKey, err := s.decryptPrivateKey(encryptedKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt private key: %w", err)
	}

	s.privateKey = privateKey
	s.address = crypto.PubkeyToAddress(privateKey.PublicKey)

	logger.Info("DEX wallet loaded by ID", "wallet_id", walletID, "address", s.address.Hex())
	return nil
}

// ExportWallet returns the decrypted private key for a wallet
func (s *WalletService) ExportWallet(ctx context.Context, userID, walletID string) (string, error) {
	var encryptedKey, address string
	err := s.pool.QueryRow(ctx, `
		SELECT private_key_encrypted, address FROM dex_wallets
		WHERE id = $1 AND user_id = $2
	`, walletID, userID).Scan(&encryptedKey, &address)
	if err != nil {
		return "", fmt.Errorf("wallet not found: %w", err)
	}

	// Decrypt private key
	privateKey, err := s.decryptPrivateKey(encryptedKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt private key: %w", err)
	}

	privateKeyHex := hex.EncodeToString(crypto.FromECDSA(privateKey))
	logger.Info("DEX wallet exported", "user_id", userID, "address", address)
	return privateKeyHex, nil
}

// GetAddress returns the loaded wallet address
func (s *WalletService) GetAddress() common.Address {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.address
}

// GetClient returns the Ethereum client
func (s *WalletService) GetClient() *ethclient.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client
}

// GetChainID returns the chain ID
func (s *WalletService) GetChainID() *big.Int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chainID
}

// GetNativeBalance returns the native token balance (ETH/BNB)
func (s *WalletService) GetNativeBalance(ctx context.Context, address string) (*big.Int, error) {
	if s.client == nil {
		return nil, fmt.Errorf("wallet service not initialized")
	}

	addr := common.HexToAddress(address)
	balance, err := s.client.BalanceAt(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	return balance, nil
}

// SignTransaction signs an Ethereum transaction
func (s *WalletService) SignTransaction(ctx context.Context, tx *types.Transaction) (*types.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.privateKey == nil {
		return nil, fmt.Errorf("wallet not loaded, cannot sign")
	}

	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(s.chainID), s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	return signedTx, nil
}

// SendTransaction broadcasts a signed transaction
func (s *WalletService) SendTransaction(ctx context.Context, signedTx *types.Transaction) (*types.Transaction, error) {
	if s.client == nil {
		return nil, fmt.Errorf("wallet service not initialized")
	}

	err := s.client.SendTransaction(ctx, signedTx)
	if err != nil {
		return nil, fmt.Errorf("failed to send transaction: %w", err)
	}

	return signedTx, nil
}

// GetWallets returns all wallets for a user
func (s *WalletService) GetWallets(ctx context.Context, userID string) ([]Wallet, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, address, chain_id, created_at, updated_at
		FROM dex_wallets
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query wallets: %w", err)
	}
	defer rows.Close()

	var wallets []Wallet
	for rows.Next() {
		var w Wallet
		err := rows.Scan(&w.ID, &w.UserID, &w.Address, &w.ChainID, &w.CreatedAt, &w.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan wallet: %w", err)
		}
		w.ChainName = s.getChainNameForID(w.ChainID)
		wallets = append(wallets, w)
	}

	return wallets, nil
}

// DexWalletKeyEnv names the secret (Infisical / compose env) that encrypts
// dex_wallets.private_key_encrypted. It is separate from ENCRYPTION_KEY so the
// exchange API keys and the wallet keys can be rotated independently.
const DexWalletKeyEnv = "DEX_WALLET_ENCRYPTION_KEY"

// ValidateDexWalletKey reports whether DexWalletKeyEnv holds a usable key.
func ValidateDexWalletKey() error {
	_, err := dexWalletKey()
	return err
}

// dexWalletKey derives the AES-256 key from DexWalletKeyEnv with HKDF-SHA256.
// There is no fallback: without the secret, wallet keys cannot be sealed or
// opened.
func dexWalletKey() ([]byte, error) {
	raw := os.Getenv(DexWalletKeyEnv)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set", DexWalletKeyEnv)
	}
	if len(raw) < 32 {
		return nil, fmt.Errorf("%s must be at least 32 characters", DexWalletKeyEnv)
	}
	key := make([]byte, 32)
	h := hkdf.New(sha256.New, []byte(raw), []byte("trading-bot-dex-wallet-encryption"), nil)
	if _, err := io.ReadFull(h, key); err != nil {
		return nil, fmt.Errorf("derive wallet key: %w", err)
	}
	return key, nil
}

// legacyDexWalletKey is the constant that was hard-coded here before
// 2026-11-03. It is public in git history, so it only exists to move old
// rows to the env key (ReencryptLegacyWalletCiphertext); it never opens
// wallets for signing.
func legacyDexWalletKey() []byte {
	return []byte("dex-wallet-encryption-key-32bytes!"[:32])
}

// ReencryptLegacyWalletCiphertext opens a ciphertext sealed with the legacy
// source key and seals it again with the env key. It fails if the input does
// not open with the legacy key, so it is safe to run over every row.
func ReencryptLegacyWalletCiphertext(encryptedHex string) (string, error) {
	key, err := dexWalletKey()
	if err != nil {
		return "", err
	}
	plaintext, err := openWalletCiphertext(legacyDexWalletKey(), encryptedHex)
	if err != nil {
		return "", fmt.Errorf("not a legacy ciphertext: %w", err)
	}
	return sealWalletCiphertext(key, plaintext)
}

// OpensWithDexWalletKey reports whether a ciphertext opens with the env key.
func OpensWithDexWalletKey(encryptedHex string) bool {
	key, err := dexWalletKey()
	if err != nil {
		return false
	}
	_, err = openWalletCiphertext(key, encryptedHex)
	return err == nil
}

// encryptPrivateKey encrypts a private key using AES-GCM
func (s *WalletService) encryptPrivateKey(privateKey *ecdsa.PrivateKey) (string, error) {
	key, err := dexWalletKey()
	if err != nil {
		return "", err
	}
	return sealWalletCiphertext(key, crypto.FromECDSA(privateKey))
}

// decryptPrivateKey decrypts a private key using AES-GCM
func (s *WalletService) decryptPrivateKey(encryptedHex string) (*ecdsa.PrivateKey, error) {
	key, err := dexWalletKey()
	if err != nil {
		return nil, err
	}
	plaintext, err := openWalletCiphertext(key, encryptedHex)
	if err != nil {
		return nil, err
	}
	privateKey, err := crypto.ToECDSA(plaintext)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	return privateKey, nil
}

// sealWalletCiphertext returns hex(nonce || AES-256-GCM ciphertext).
func sealWalletCiphertext(key, plaintext []byte) (string, error) {
	aesGCM, err := newWalletGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	return hex.EncodeToString(aesGCM.Seal(nonce, nonce, plaintext, nil)), nil
}

// openWalletCiphertext reverses sealWalletCiphertext.
func openWalletCiphertext(key []byte, encryptedHex string) ([]byte, error) {
	encryptedBytes, err := hex.DecodeString(encryptedHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode encrypted key: %w", err)
	}
	aesGCM, err := newWalletGCM(key)
	if err != nil {
		return nil, err
	}
	nonceSize := aesGCM.NonceSize()
	if len(encryptedBytes) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := encryptedBytes[:nonceSize], encryptedBytes[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}
	return plaintext, nil
}

func newWalletGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}
	return aesGCM, nil
}

// getChainName returns the name of the configured default chain
func (s *WalletService) getChainName() string {
	if chainCfg, ok := s.cfg.Chains[s.cfg.DefaultChain]; ok {
		return chainCfg.Name
	}
	return "Unknown"
}

// getChainNameForID returns the name for a specific chain ID
func (s *WalletService) getChainNameForID(chainID int64) string {
	for id, chainCfg := range s.cfg.Chains {
		if int64(id) == chainID {
			return chainCfg.Name
		}
	}
	return "Unknown"
}
