package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	vaultKeySize = 32
	vaultPrefix  = "owv1:"
	vaultKeyFile = "vault.key"
)

var vaultAAD = []byte("oldwangvideo:vault:v1")

// Vault 使用本地密钥对写入数据库的敏感文本进行加解密。
type Vault struct {
	mu  sync.RWMutex
	key []byte
}

// NewVault 在数据目录中加载或创建权限为 0600 的 AES-256 密钥。
func NewVault(dataDir string) (*Vault, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("数据目录不能为空")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("设置数据目录权限: %w", err)
	}

	key, err := loadOrCreateKey(filepath.Join(dataDir, vaultKeyFile))
	if err != nil {
		return nil, err
	}
	return &Vault{key: key}, nil
}

// Encrypt 将明文编码为带版本前缀的 AES-GCM 密文。
func (v *Vault) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	v.mu.RLock()
	defer v.mu.RUnlock()
	gcm, err := v.gcm()
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成加密随机数: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), vaultAAD)
	return vaultPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt 校验版本与认证标签后返回明文。
func (v *Vault) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if !strings.HasPrefix(ciphertext, vaultPrefix) {
		return "", errors.New("不支持的密文版本")
	}

	encoded := strings.TrimPrefix(ciphertext, vaultPrefix)
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("解析密文: %w", err)
	}

	v.mu.RLock()
	defer v.mu.RUnlock()
	gcm, err := v.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("密文长度无效")
	}
	nonce, payload := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, payload, vaultAAD)
	if err != nil {
		return "", errors.New("密文校验失败")
	}
	return string(plaintext), nil
}

// Close 清除进程内持有的密钥副本。
func (v *Vault) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.key {
		v.key[i] = 0
	}
	v.key = nil
}

func (v *Vault) gcm() (cipher.AEAD, error) {
	if len(v.key) != vaultKeySize {
		return nil, errors.New("Vault 已关闭或密钥无效")
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM: %w", err)
	}
	return gcm, nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != vaultKeySize {
			return nil, fmt.Errorf("Vault 密钥长度应为 %d 字节", vaultKeySize)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("设置 Vault 密钥权限: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 Vault 密钥: %w", err)
	}

	key = make([]byte, vaultKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("生成 Vault 密钥: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("创建 Vault 密钥: %w", err)
	}

	written, writeErr := file.Write(key)
	if writeErr == nil && written != len(key) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("写入 Vault 密钥: %w", writeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("关闭 Vault 密钥文件: %w", closeErr)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("设置 Vault 密钥权限: %w", err)
	}
	return key, nil
}
