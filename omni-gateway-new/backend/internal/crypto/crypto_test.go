package crypto

import (
	"strings"
	"testing"
)

func TestCipherRotation(t *testing.T) {
	oldKey, newKey := strings.Repeat("11", 32), strings.Repeat("22", 32)
	old, _ := NewCipher(oldKey)
	newCipher, _ := NewCipher(newKey)
	ct, err := old.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	rotating, err := NewRotatingCipher(newKey, []string{oldKey})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := rotating.Decrypt(ct)
	if err != nil || plain != "secret" {
		t.Fatal("old ciphertext not accepted")
	}
	ct, err = rotating.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Decrypt(ct); err == nil {
		t.Fatal("still encrypted with old key")
	}
	if v, err := newCipher.Decrypt(ct); err != nil || v != plain {
		t.Fatal("new ciphertext invalid")
	}
	if _, err := NewRotatingCipher(newKey, []string{"bad"}); err == nil {
		t.Fatal("accepted bad previous key")
	}
}

func TestCryptoInvalidInputs(t *testing.T) {
	if _, err := NewCipher("bad"); err == nil {
		t.Fatal("accepted key")
	}
	c, _ := NewCipher(strings.Repeat("11", 32))
	for _, v := range []string{"!", "", "eA==", strings.Repeat("A", 64)} {
		if _, err := c.Decrypt(v); err == nil {
			t.Fatal("accepted ciphertext")
		}
	}
	if HashAPIKey("a") == HashAPIKey("b") {
		t.Fatal("same key hashes")
	}
}

func TestPasswordValidation(t *testing.T) {
	h, err := HashPassword("test password")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword("test password", h); !ok || err != nil {
		t.Fatal("password mismatch")
	}
	if ok, _ := VerifyPassword("wrong", h); ok {
		t.Fatal("wrong password accepted")
	}
	for _, v := range []string{"bad", strings.Replace(h, "v=19", "v=18", 1), "$argon2id$v=19$m=65536,t=1,p=4$$"} {
		if ok, err := VerifyPassword("", v); ok || err == nil {
			t.Fatal("invalid hash accepted")
		}
	}
}
