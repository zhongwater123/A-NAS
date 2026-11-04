package accounts

// SetPasswordVerifier replaces how sign-ins check passwords, so that tests
// can count and hold the argon2id computations.
func SetPasswordVerifier(s *Service, verify func(password, encoded string) bool) {
	s.passwords.verify = verify
}

// VerifyPassword is the check SetPasswordVerifier replaces.
var VerifyPassword = verifyPassword
