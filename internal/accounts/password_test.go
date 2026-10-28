package accounts

import (
	"bytes"
	"testing"
)

var cheapCost = PasswordCost{MemoryKiB: 64, Iterations: 1, Parallelism: 1}

func TestAPasswordIsVerifiedAtTheCostItWasSetWith(t *testing.T) {
	t.Parallel()
	for _, cost := range []PasswordCost{cheapCost, {MemoryKiB: 128, Iterations: 2, Parallelism: 2}} {
		encoded, err := hashPassword("correct horse battery staple", bytes.NewReader(make([]byte, 16)), cost)
		if err != nil {
			t.Fatal(err)
		}
		if !verifyPassword("correct horse battery staple", encoded) || verifyPassword("wrong horse battery staple", encoded) {
			t.Fatalf("verifyPassword() of %s gave the wrong answer", encoded)
		}
		if hash, ok := parsePasswordHash(encoded); !ok || hash.cost != cost {
			t.Fatalf("parsePasswordHash(%s) = %+v, %v; want cost %+v", encoded, hash.cost, ok, cost)
		}
	}
}

// FuzzParsePasswordHash reads stored hashes, which a damaged or old database
// may hold in any form: parsing never panics, and anything it accepts names
// a usable cost and key.
func FuzzParsePasswordHash(f *testing.F) {
	f.Add("$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U")
	f.Add("$argon2id$v=19$m=0,t=3,p=2$$")
	f.Add("$argon2id$v=19$m=65536,t=3,p=256$c2FsdA$a2V5")
	f.Add("$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$a2V5")
	f.Add("")
	f.Fuzz(func(t *testing.T, encoded string) {
		hash, ok := parsePasswordHash(encoded)
		if ok && (hash.cost.MemoryKiB == 0 || hash.cost.Iterations == 0 || hash.cost.Parallelism == 0 || len(hash.key) == 0) {
			t.Fatalf("parsePasswordHash(%q) accepted %+v", encoded, hash)
		}
	})
}
