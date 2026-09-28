// Package security holds the password hashing adapter.
package security

import "golang.org/x/crypto/bcrypt"

// Bcrypt implements ports.PasswordHasher.
type Bcrypt struct{}

func (Bcrypt) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func (Bcrypt) Compare(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
