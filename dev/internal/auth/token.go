package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
)

// Token 使用原版 token.txt 的原始 SHA-256 字节格式，不保存明文。
type Token struct {
	hash   [sha256.Size]byte
	UserID string
}

func Load(path string, generate bool) (*Token, string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if !generate {
			return nil, "", nil
		}
		plain := rand.Text()[:12]
		hash := sha256.Sum256([]byte(plain))
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(createErr) {
			return Load(path, false)
		}
		if createErr != nil {
			return nil, "", createErr
		}
		_, writeErr := file.Write(hash[:])
		closeErr := file.Close()
		if writeErr != nil {
			return nil, "", writeErr
		}
		if closeErr != nil {
			return nil, "", closeErr
		}
		return &Token{hash: hash}, plain, nil
	}
	if err != nil {
		return nil, "", err
	}
	if len(data) != sha256.Size {
		return nil, "", fmt.Errorf("token.txt 必须是原版格式的 32 字节 SHA-256 文件，不能填写明文；可停止程序并删除该文件后重新生成")
	}
	var hash [sha256.Size]byte
	copy(hash[:], data)
	return &Token{hash: hash}, "", nil
}

func (t *Token) Valid(plain string) bool {
	if t == nil || plain == "" || len(plain) > 4096 {
		return false
	}
	hash := sha256.Sum256([]byte(plain))
	return subtle.ConstantTimeCompare(hash[:], t.hash[:]) == 1
}

func (t *Token) Fingerprint() string { return hex.EncodeToString(t.hash[:]) }
