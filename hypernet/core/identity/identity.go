package identity

import (
	"errors"
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// LoadOrCreatePrivateKey loads a libp2p private key from the given path,
// or generates and saves a new Ed25519 key if the file does not exist.
func LoadOrCreatePrivateKey(path string) (crypto.PrivKey, error) {
	if path == "" {
		return nil, errors.New("empty key path")
	}

	data, err := os.ReadFile(path)
	if err == nil {
		// Existing key.
		return crypto.UnmarshalPrivateKey(data)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Generate a new key.
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, err
	}

	encoded, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, err
	}

	if writeErr := os.WriteFile(path, encoded, 0o600); writeErr != nil {
		return nil, writeErr
	}

	return priv, nil
}

