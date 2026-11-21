package auth

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	minPasswordRunes = 8  // tối thiểu 8 ký tự
	maxPasswordBytes = 72 // giới hạn của bcrypt: vượt thì bị cắt im lặng, nên từ chối luôn
)

var (
	ErrInvalidUsername = errors.New("auth: username phải gồm 3-32 ký tự a-z, 0-9 hoặc _")
	ErrInvalidPassword = errors.New("auth: mật khẩu phải từ 8 ký tự và không quá 72 byte")

	usernameRE = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)
)

// NormalizeUsername đưa username về dạng chuẩn (bỏ khoảng trắng hai đầu, chữ thường)
// để "Alice" và "alice " là cùng một tài khoản.
func NormalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateUsername kiểm tra username đã được chuẩn hoá.
func ValidateUsername(u string) error {
	if !usernameRE.MatchString(u) {
		return ErrInvalidUsername
	}
	return nil
}

// ValidatePassword kiểm tra độ dài: tối thiểu 8 ký tự (rune), tối đa 72 byte.
func ValidatePassword(p string) error {
	if utf8.RuneCountInString(p) < minPasswordRunes || len(p) > maxPasswordBytes {
		return ErrInvalidPassword
	}
	return nil
}
