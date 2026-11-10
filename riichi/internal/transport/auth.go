package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"summermahjong/riichi/internal/game"
)

// JWT 采用 HS256（HMAC-SHA256），只用标准库实现，不引入第三方认证库。
//
// 令牌结构：header.payload.signature，三段均为 base64url（无 padding）。
//
//	header: {"alg":"HS256","typ":"JWT"}
//	payload: {"sub":"0","seat":0,"type":"seat","iat":...,"exp":...}
//
// 座位绑定：seat / sub 即座位号（0-3）。HTTP 侧从 Authorization: Bearer 取，
// WS 侧从 ?token= 取（浏览器 WebSocket API 不能自定义 header）。
//
// 安全细节：
//   - 验签用 hmac.Equal（常数时间比较），避免时序侧信道；
//   - 显式拒绝 alg != HS256（含 "none"），防止算法混淆攻击；
//   - 校验 exp，过期返回 ErrTokenExpired，调用方映射 401。
const (
	jwtAlg      = "HS256"
	jwtTyp      = "JWT"
	jwtTokenTyp = "seat"
)

// 令牌相关哨兵错误。
var (
	// ErrTokenMissing 表示请求没有携带令牌。
	ErrTokenMissing = errors.New("transport: token missing")
	// ErrTokenInvalid 表示令牌格式/签名/声明非法。
	ErrTokenInvalid = errors.New("transport: token invalid")
	// ErrTokenExpired 表示令牌已过期。
	ErrTokenExpired = errors.New("transport: token expired")
)

// jwtHeader 是 JWT 头部。
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// jwtClaims 是 JWT 载荷（契约之外的自定义声明，保持最小）。
type jwtClaims struct {
	Sub  string `json:"sub"`            // 座位号文本
	Seat *int   `json:"seat,omitempty"` // 座位号（0-3）
	Typ  string `json:"type,omitempty"` // 令牌用途：seat
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
}

// SignToken 为座位签发 JWT，ttl 为有效期。
//
// 用法（服务无内置登录接口，S6 只做座位令牌）：
//
//	token, _ := transport.SignToken(secret, game.Seat(0), 2*time.Hour)
func SignToken(secret []byte, seat game.Seat, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("transport: ttl must be positive, got %v", ttl)
	}
	return signToken(secret, seat, time.Now(), ttl)
}

// signToken 用显式签发时间生成令牌；ttl 可为负（测试过期令牌用）。
func signToken(secret []byte, seat game.Seat, now time.Time, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("transport: jwt secret is empty")
	}
	if seat >= seatCount {
		return "", fmt.Errorf("transport: invalid seat %d", seat)
	}

	seatInt := int(seat)
	headerJSON, err := json.Marshal(jwtHeader{Alg: jwtAlg, Typ: jwtTyp})
	if err != nil {
		return "", fmt.Errorf("transport: marshal jwt header: %w", err)
	}
	claimsJSON, err := json.Marshal(jwtClaims{
		Sub:  strconv.Itoa(seatInt),
		Seat: &seatInt,
		Typ:  jwtTokenTyp,
		Iat:  now.Unix(),
		Exp:  now.Add(ttl).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("transport: marshal jwt claims: %w", err)
	}

	signingInput := encodeSegment(headerJSON) + "." + encodeSegment(claimsJSON)
	return signingInput + "." + encodeSegment(sign(secret, signingInput)), nil
}

// ParseToken 校验 JWT 并返回座位号。
func ParseToken(secret []byte, token string) (game.Seat, error) {
	if len(secret) == 0 {
		return 0, errors.New("transport: jwt secret is empty")
	}
	if token == "" {
		return 0, ErrTokenMissing
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("%w: 需要 header.payload.signature 三段", ErrTokenInvalid)
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, fmt.Errorf("%w: header 不是合法 base64url", ErrTokenInvalid)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return 0, fmt.Errorf("%w: header 不是合法 JSON", ErrTokenInvalid)
	}
	if header.Alg != jwtAlg {
		// 拒绝 "none" / RS256 等其它算法，防止算法混淆
		return 0, fmt.Errorf("%w: 不支持的签名算法 %q", ErrTokenInvalid, header.Alg)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return 0, fmt.Errorf("%w: signature 不是合法 base64url", ErrTokenInvalid)
	}
	expected := sign(secret, parts[0]+"."+parts[1])
	if !hmac.Equal(signature, expected) {
		return 0, fmt.Errorf("%w: 签名不匹配", ErrTokenInvalid)
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, fmt.Errorf("%w: payload 不是合法 base64url", ErrTokenInvalid)
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return 0, fmt.Errorf("%w: payload 不是合法 JSON", ErrTokenInvalid)
	}
	if claims.Seat == nil {
		return 0, fmt.Errorf("%w: 缺少 seat 声明", ErrTokenInvalid)
	}
	if *claims.Seat < 0 || *claims.Seat >= seatCount {
		return 0, fmt.Errorf("%w: seat 超出范围 %d", ErrTokenInvalid, *claims.Seat)
	}
	if claims.Typ != jwtTokenTyp {
		return 0, fmt.Errorf("%w: 令牌用途不是座位令牌", ErrTokenInvalid)
	}
	if claims.Sub != "" && claims.Sub != strconv.Itoa(*claims.Seat) {
		return 0, fmt.Errorf("%w: sub 与 seat 不一致", ErrTokenInvalid)
	}
	if claims.Exp <= time.Now().Unix() {
		return 0, ErrTokenExpired
	}
	return game.Seat(*claims.Seat), nil
}

// sign 计算 HMAC-SHA256 签名。
func sign(secret []byte, signingInput string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

// encodeSegment 输出无填充的 base64url。
func encodeSegment(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// tokenFromRequest 从 Authorization: Bearer 或 ?token= 取令牌（WS 用 query）。
func tokenFromRequest(header string, queryToken string) string {
	if header != "" {
		if trimmed := strings.TrimSpace(header); len(trimmed) > 7 && strings.EqualFold(trimmed[:7], "bearer ") {
			return strings.TrimSpace(trimmed[7:])
		}
	}
	return queryToken
}
