package transport

import (
	"errors"
	"strings"
	"testing"
	"time"

	"summermahjong/riichi/internal/game"
)

// TestSignAndParseToken 断言签发/解析往返得到同一座位。
func TestSignAndParseToken(t *testing.T) {
	secret := []byte("unit-test-secret")
	for seat := game.Seat(0); seat < seatCount; seat++ {
		token, err := SignToken(secret, seat, time.Hour)
		if err != nil {
			t.Fatalf("SignToken(%d): %v", seat, err)
		}
		got, err := ParseToken(secret, token)
		if err != nil {
			t.Fatalf("ParseToken(%d): %v", seat, err)
		}
		if got != seat {
			t.Fatalf("座位往返不一致: want %d got %d", seat, got)
		}
	}
}

// TestParseTokenExpired 断言过期令牌返回 ErrTokenExpired。
func TestParseTokenExpired(t *testing.T) {
	secret := []byte("unit-test-secret")
	token, err := signToken(secret, 1, time.Now(), -time.Minute)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, err := ParseToken(secret, token); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("期望 ErrTokenExpired，实际 %v", err)
	}
}

// TestParseTokenTampered 断言篡改签名后返回 ErrTokenInvalid。
func TestParseTokenTampered(t *testing.T) {
	secret := []byte("unit-test-secret")
	token, err := SignToken(secret, 2, time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("令牌不是三段式: %q", token)
	}
	parts[2] = parts[2][:len(parts[2])-1] + "A"
	if _, err := ParseToken(secret, strings.Join(parts, ".")); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("期望 ErrTokenInvalid，实际 %v", err)
	}
}

// TestParseTokenWrongSecret 断言换了密钥后验签失败。
func TestParseTokenWrongSecret(t *testing.T) {
	token, err := SignToken([]byte("secret-a"), 0, time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, err := ParseToken([]byte("secret-b"), token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("期望 ErrTokenInvalid，实际 %v", err)
	}
}

// TestParseTokenRejectsNoneAlg 断言拒绝 alg=none（算法混淆攻击）。
func TestParseTokenRejectsNoneAlg(t *testing.T) {
	// {"alg":"none","typ":"JWT"} / {"sub":"0","seat":0,"type":"seat","exp":9999999999}
	header := encodeSegment([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := encodeSegment([]byte(`{"sub":"0","seat":0,"type":"seat","iat":1,"exp":9999999999}`))
	token := header + "." + claims + "."
	if _, err := ParseToken([]byte("unit-test-secret"), token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("期望 ErrTokenInvalid，实际 %v", err)
	}
}

// TestParseTokenMalformed 断言格式错误/缺字段/座位越界的令牌都被拒绝。
func TestParseTokenMalformed(t *testing.T) {
	secret := []byte("unit-test-secret")
	cases := map[string]string{
		"空令牌":   "",
		"不是三段式": "abc.def",
		"乱码段":   "!!!.???.###",
		"缺少 seat": encodeSegment([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." +
			encodeSegment([]byte(`{"type":"seat","exp":9999999999}`)) + ".sig",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseToken(secret, token); err == nil {
				t.Fatalf("令牌 %q 不应通过校验", token)
			}
		})
	}

	// 座位越界：签名正确但 seat=9
	token, err := SignToken(secret, 0, time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, err := ParseToken(secret, token); err != nil {
		t.Fatalf("合法令牌不应报错: %v", err)
	}
}

// TestSignTokenValidation 断言签发参数校验。
func TestSignTokenValidation(t *testing.T) {
	if _, err := SignToken(nil, 0, time.Hour); err == nil {
		t.Fatal("空密钥应报错")
	}
	if _, err := SignToken([]byte("k"), game.Seat(4), time.Hour); err == nil {
		t.Fatal("非法座位应报错")
	}
	if _, err := SignToken([]byte("k"), 0, 0); err == nil {
		t.Fatal("非正 ttl 应报错")
	}
}

// TestTokenFromRequest 断言 Bearer 头与 query 参数的取值优先级。
func TestTokenFromRequest(t *testing.T) {
	cases := []struct {
		name   string
		header string
		query  string
		want   string
	}{
		{"Bearer 头", "Bearer abc", "", "abc"},
		{"大小写不敏感", "bearer abc", "", "abc"},
		{"query 兜底", "", "xyz", "xyz"},
		{"头优先", "Bearer abc", "xyz", "abc"},
		{"非 Bearer", "Basic abc", "xyz", "xyz"},
		{"都为空", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenFromRequest(tc.header, tc.query); got != tc.want {
				t.Fatalf("tokenFromRequest(%q,%q)=%q want %q", tc.header, tc.query, got, tc.want)
			}
		})
	}
}

// TestParseSeat 断言座位参数解析。
func TestParseSeat(t *testing.T) {
	for _, text := range []string{"0", "1", "2", "3"} {
		if _, err := parseSeat(text); err != nil {
			t.Fatalf("parseSeat(%q) 不应报错: %v", text, err)
		}
	}
	for _, text := range []string{"", "4", "-1", "east", "1.5"} {
		if _, err := parseSeat(text); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("parseSeat(%q) 期望 ErrInvalidRequest，实际 %v", text, err)
		}
	}
}
