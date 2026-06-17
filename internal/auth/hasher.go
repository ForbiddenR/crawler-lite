package auth

import "golang.org/x/crypto/bcrypt"

// Hasher is the password-hashing API used by Service. Wrapping bcrypt behind an
// interface lets tests inject a fast no-op hasher and lets us swap algorithms
// in the future without changing call sites.
type Hasher interface {
	Hash(password string) (string, error)
	Compare(hash, password string) error
}

type bcryptHasher struct{ cost int }

// NewBcryptHasher returns a Hasher backed by bcrypt at the given cost.
// Cost 10 is the recommended default; cost 12+ for production.
func NewBcryptHasher(cost int) Hasher {
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	return &bcryptHasher{cost: cost}
}

func (h *bcryptHasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (h *bcryptHasher) Compare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
