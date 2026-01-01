package envmanager

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

type Manager struct {
	dataPath string
	envKey   []byte
}

func New(dataPath string, password string) *Manager {
	envKey := pbkdf2.Key([]byte(password), []byte("bunshin-v1-salt"), 4096, 32, sha256.New)
	return &Manager{
		dataPath: dataPath,
		envKey:   envKey,
	}
}

func (m *Manager) Encrypt(data []byte) ([]byte, error) {
	block, _ := aes.NewCipher(m.envKey)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	return gcm.Seal(nonce, nonce, data, nil), nil
}

func (m *Manager) Decrypt(data []byte) ([]byte, error) {
	block, _ := aes.NewCipher(m.envKey)
	gcm, _ := cipher.NewGCM(block)
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, data[:ns], data[ns:], nil)
}

func (m *Manager) ParseEnvFile(envContent string) map[string]string {
	envMap := make(map[string]string)
	lines := strings.SplitSeq(envContent, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				value = value[1 : len(value)-1]
			}
			envMap[key] = value
		}
	}
	return envMap
}

func (m *Manager) ReadEnv(name string) (string, error) {
	enc, err := os.ReadFile(filepath.Join(m.dataPath, "env", name+".env"))
	if err != nil {
		return "", err
	}
	dec, err := m.Decrypt(enc)
	if err != nil {
		return "", err
	}
	return string(dec), nil
}

func (m *Manager) WriteEnv(name string, envContent string) error {
	enc, err := m.Encrypt([]byte(envContent))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.dataPath, "env", name+".env"), enc, 0644)
}

func (m *Manager) GetEnvMap(name string) map[string]string {
	envStr, err := m.ReadEnv(name)
	if err != nil {
		return make(map[string]string)
	}
	return m.ParseEnvFile(envStr)
}
