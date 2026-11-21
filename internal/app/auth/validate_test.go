package auth

import (
	"strings"
	"testing"
)

func TestNormalizeUsername(t *testing.T) {
	for in, want := range map[string]string{
		"Alice":      "alice",
		"  bob  ":    "bob",
		"CAROL_99":   "carol_99",
		"\tdave\n":   "dave",
		"already_ok": "already_ok",
	} {
		if got := NormalizeUsername(in); got != want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"3 ký tự (tối thiểu)", "abc", true},
		{"32 ký tự (tối đa)", strings.Repeat("a", 32), true},
		{"có số và gạch dưới", "user_42", true},
		{"2 ký tự", "ab", false},
		{"33 ký tự", strings.Repeat("a", 33), false},
		{"rỗng", "", false},
		{"có khoảng trắng", "a b c", false},
		{"có dấu chấm", "a.b", false},
		{"chữ hoa (phải chuẩn hoá trước)", "Alice", false},
		{"có dấu tiếng Việt", "đức", false},
		{"ký tự đặc biệt", "bob!", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUsername(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("ValidateUsername(%q) lỗi = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"8 ký tự (tối thiểu)", "12345678", true},
		{"7 ký tự", "1234567", false},
		{"rỗng", "", false},
		{"72 byte (tối đa)", strings.Repeat("a", 72), true},
		{"73 byte", strings.Repeat("a", 73), false},
		// "é" là 2 byte: 30 ký tự = 60 byte, hợp lệ
		{"nhiều byte mỗi ký tự, trong giới hạn", strings.Repeat("é", 30), true},
		// 40 ký tự = 80 byte > 72: phải từ chối thay vì để bcrypt cắt im lặng
		{"nhiều byte mỗi ký tự, vượt 72 byte", strings.Repeat("é", 40), false},
		{"7 ký tự tiếng Việt", "mậtkhẩu", false},
		{"8 ký tự tiếng Việt", "mậtkhẩu1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("ValidatePassword(%q) lỗi = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}
