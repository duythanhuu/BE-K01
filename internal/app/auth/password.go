// Package auth xử lý đăng ký, đăng nhập và session. Mọi trạng thái nằm trong Redis.
package auth

import (
	"sync"

	"golang.org/x/crypto/bcrypt"
)

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

// dummyHash là một chuỗi băm hợp lệ không thuộc về ai. Đăng nhập với username
// không tồn tại vẫn so khớp với nó, để thời gian phản hồi bằng trường hợp
// "sai mật khẩu" (bcrypt chạy cả hai nhánh) và không lộ username nào có thật.
// Tính lười ở lần dùng đầu để dùng đúng bcryptCost hiện hành.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword("mật-khẩu-giả-chỉ-để-cân-bằng-thời-gian")
	if err != nil {
		panic(err) // chỉ xảy ra nếu bcryptCost không hợp lệ: lỗi lập trình
	}
	return h
})

// CheckPassword báo mật khẩu có khớp với chuỗi băm không (so sánh thời gian cố định).
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
