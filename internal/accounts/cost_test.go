package accounts_test

import "github.com/zhongwater123/A-NAS/internal/accounts"

// cheapPasswords keeps argon2id fast in tests; production uses
// accounts.DefaultPasswordCost (#223).
var cheapPasswords = accounts.PasswordCost{MemoryKiB: 64, Iterations: 1, Parallelism: 1}
