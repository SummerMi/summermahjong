package transport

import (
	"errors"
	"fmt"
	"net/http"

	"summermahjong/riichi/internal/game"
)

// 错误码（契约 §7）。HTTP 与 WS 共用同一套字符串，便于前端按 code 分支。
const (
	CodeIllegalAction = "ILLEGAL_ACTION"
	CodeStaleSeq      = "STALE_SEQ"
	CodeSeatMismatch  = "SEAT_MISMATCH"
	CodeGameNotFound  = "GAME_NOT_FOUND"
	CodeInternal      = "INTERNAL"
	CodeValidation    = "VALIDATION"
	CodeUnauthorized  = "UNAUTHORIZED"
)

// 传输层哨兵错误；调用方用 errors.Is 判断。
var (
	// ErrStaleSeq 表示客户端 action.seq 与当前状态不一致，需要先 resume。
	ErrStaleSeq = errors.New("transport: stale seq, resume first")
	// ErrSeatMismatch 表示令牌座位与请求座位不一致。
	ErrSeatMismatch = errors.New("transport: seat mismatch")
	// ErrGameNotFound 表示对局不存在。
	ErrGameNotFound = errors.New("transport: game not found")
	// ErrRoomClosed 表示对局房间已关闭（服务停止或对局被清理）。
	ErrRoomClosed = errors.New("transport: room closed")
)

// apiErrorBody 是 HTTP 错误响应体：{"error":{"code","message"}}。
type apiErrorBody struct {
	Error apiError `json:"error"`
}

// apiError 是错误详情。
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// httpStatusFor 把传输层错误映射为 HTTP 状态码。
func httpStatusFor(err error) int {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrSeatMismatch):
		return http.StatusForbidden
	case errors.Is(err, ErrTokenMissing), errors.Is(err, ErrTokenInvalid), errors.Is(err, ErrTokenExpired):
		return http.StatusUnauthorized
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrStaleSeq), errors.Is(err, game.ErrIllegalAction):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// errorCodeFor 把传输层错误映射为契约错误码。
func errorCodeFor(err error) string {
	switch {
	case errors.Is(err, ErrGameNotFound):
		return CodeGameNotFound
	case errors.Is(err, ErrSeatMismatch):
		return CodeSeatMismatch
	case errors.Is(err, ErrTokenMissing), errors.Is(err, ErrTokenInvalid), errors.Is(err, ErrTokenExpired):
		return CodeUnauthorized
	case errors.Is(err, ErrInvalidRequest):
		return CodeValidation
	case errors.Is(err, ErrStaleSeq):
		return CodeStaleSeq
	case errors.Is(err, game.ErrIllegalAction):
		return CodeIllegalAction
	default:
		return CodeInternal
	}
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = jsonEncode(w, v)
}

// writeAPIError 输出统一错误信封。
func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorBody{Error: apiError{Code: code, Message: message}})
}

// writeError 把 error 映射为 HTTP 状态码 + 错误码后输出。
func writeError(w http.ResponseWriter, err error) {
	writeAPIError(w, httpStatusFor(err), errorCodeFor(err), err.Error())
}

// wrapSeatMismatch 构造带详细信息的座位不匹配错误。
func wrapSeatMismatch(tokenSeat, requestSeat game.Seat) error {
	return fmt.Errorf("%w: 令牌座位为 %d，请求座位为 %d", ErrSeatMismatch, tokenSeat, requestSeat)
}
