package utils

import "golang.org/x/crypto/bcrypt"


// HashPassword uses bcrypt to securely hash a password
func HashPassword(password string) (string, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err // return the error if the password fails to hash
	}
	// return the hashed password as a string
	return string(hashedPassword), nil
}


// CheckPassword checks if a password matches a hash
func CheckPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
} // return true if the password matches the hash, false otherwise