package service

import (
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

const testWalletKey = "test-dex-wallet-key-0123456789abcdef"

func TestDexWalletKeyMissingFailsClosed(t *testing.T) {
	t.Setenv(DexWalletKeyEnv, "")
	s := &WalletService{}
	pk, _ := crypto.GenerateKey()

	if _, err := s.encryptPrivateKey(pk); err == nil {
		t.Fatal("encrypt must fail when " + DexWalletKeyEnv + " is unset")
	}
	if _, err := s.decryptPrivateKey("00"); err == nil {
		t.Fatal("decrypt must fail when " + DexWalletKeyEnv + " is unset")
	}
}

func TestDexWalletKeyTooShortFailsClosed(t *testing.T) {
	t.Setenv(DexWalletKeyEnv, "short")
	if err := ValidateDexWalletKey(); err == nil {
		t.Fatal("a key shorter than 32 characters must be rejected")
	}
}

func TestDexWalletKeyComesFromEnv(t *testing.T) {
	t.Setenv(DexWalletKeyEnv, testWalletKey)
	s := &WalletService{}
	pk, _ := crypto.GenerateKey()

	enc, err := s.encryptPrivateKey(pk)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := s.decryptPrivateKey(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got.D.Cmp(pk.D) != 0 {
		t.Fatal("round trip changed the key")
	}

	// The constant that used to live in source must not open new ciphertexts.
	if _, err := openWalletCiphertext(legacyDexWalletKey(), enc); err == nil {
		t.Fatal("new ciphertext opens with the legacy source key")
	}

	// A different env key must not open it either.
	t.Setenv(DexWalletKeyEnv, testWalletKey+"-other")
	if _, err := s.decryptPrivateKey(enc); err == nil {
		t.Fatal("ciphertext opened with a different key")
	}
}

func TestReencryptLegacyWalletCiphertext(t *testing.T) {
	t.Setenv(DexWalletKeyEnv, testWalletKey)
	s := &WalletService{}
	pk, _ := crypto.GenerateKey()

	legacy, err := sealWalletCiphertext(legacyDexWalletKey(), crypto.FromECDSA(pk))
	if err != nil {
		t.Fatalf("seal legacy: %v", err)
	}
	if _, err := s.decryptPrivateKey(legacy); err == nil {
		t.Fatal("legacy ciphertext must not open with the env key (no silent fallback)")
	}

	rekeyed, err := ReencryptLegacyWalletCiphertext(legacy)
	if err != nil {
		t.Fatalf("reencrypt: %v", err)
	}
	got, err := s.decryptPrivateKey(rekeyed)
	if err != nil {
		t.Fatalf("decrypt rekeyed: %v", err)
	}
	if got.D.Cmp(pk.D) != 0 {
		t.Fatal("re-encryption changed the key")
	}

	if OpensWithDexWalletKey(legacy) || !OpensWithDexWalletKey(rekeyed) {
		t.Fatal("OpensWithDexWalletKey must tell legacy rows from rekeyed rows")
	}
	if _, err := ReencryptLegacyWalletCiphertext(rekeyed); err == nil {
		t.Fatal("a row already on the env key must not be treated as legacy")
	}
}
