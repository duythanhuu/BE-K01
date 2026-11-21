package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func init() {
	// Hạ cost xuống mức thấp nhất để test chạy nhanh (không ảnh hưởng code thật).
	bcryptCost = bcrypt.MinCost
}

func TestHashPassword_NotPlaintext(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if h == "correct horse" || strings.Contains(h, "correct horse") {
		t.Fatalf("hash chứa mật khẩu gốc: %q", h)
	}
	if !strings.HasPrefix(h, "$2") {
		t.Fatalf("không phải định dạng bcrypt: %q", h)
	}
}

func TestHashPassword_SaltedSoHashesDiffer(t *testing.T) {
	a, _ := HashPassword("same-password")
	b, _ := HashPassword("same-password")
	if a == b {
		t.Fatal("cùng mật khẩu phải cho hai chuỗi băm khác nhau (muối ngẫu nhiên)")
	}
}

func TestCheckPassword(t *testing.T) {
	h, _ := HashPassword("correct horse")

	if !CheckPassword(h, "correct horse") {
		t.Error("mật khẩu đúng bị từ chối")
	}
	if CheckPassword(h, "wrong horse") {
		t.Error("mật khẩu sai được chấp nhận")
	}
	if CheckPassword("không-phải-hash", "correct horse") {
		t.Error("hash hỏng không được chấp nhận")
	}
}
