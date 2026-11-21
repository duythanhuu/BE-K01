// Package auth xử lý đăng ký, đăng nhập và session. Mọi trạng thái nằm trong Redis.
package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost là độ tốn kém của bcrypt (cost 10 ≈ vài chục ms mỗi lần băm).
// Là biến để test hạ xuống bcrypt.MinCost cho nhanh.
var bcryptCost = bcrypt.DefaultCost

// HashPassword băm mật khẩu bằng bcrypt. Muối ngẫu nhiên được sinh và nhúng sẵn
// trong chuỗi kết quả, nên cùng một mật khẩu cho ra hai chuỗi khác nhau.
// Mật khẩu phải qua ValidatePassword trước (bcrypt chỉ đọc 72 byte đầu).
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword báo mật khẩu có khớp với chuỗi băm không (so sánh thời gian cố định).
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
