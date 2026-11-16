// Command riichi-server 启动 riichi 对局服务（HTTP + WebSocket）。
//
// 用法：
//
//	# 启动服务（JWT 密钥必填，建议通过环境变量注入）
//	RIICHI_JWT_SECRET=change-me go run ./cmd/riichi-server -addr :8080
//
//	# 只签发座位令牌（调试 / 前端联调用）后退出
//	go run ./cmd/riichi-server -jwt-secret change-me -issue-token 0
//
// 注意：M1 没有登录系统，令牌由运维/调试侧签发；生产应改成账号体系登录后签发。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"summermahjong/riichi/internal/game"
	redisstore "summermahjong/riichi/internal/store/redis"
	"summermahjong/riichi/internal/transport"

	goredis "github.com/redis/go-redis/v9"
)

// devJWTSecret 是未显式配置密钥时的开发默认值（仅本机 demo，生产必须覆盖）。
const devJWTSecret = "dev-secret-change-me-32-bytes-minimum"

func main() {
	addr := flag.String("addr", ":8080", "HTTP/WS 监听地址")
	secret := flag.String("jwt-secret", os.Getenv("RIICHI_JWT_SECRET"), "JWT 签名密钥（也可用 RIICHI_JWT_SECRET）")
	agentTimeout := flag.Duration("agent-timeout", 500*time.Millisecond, "AI 单次决策上限（契约默认 500ms）")
	issueToken := flag.Int("issue-token", -1, "只签发指定座位的 JWT（0-3）并退出")
	tokenTTL := flag.Duration("token-ttl", 2*time.Hour, "签发令牌的有效期")
	corsOrigin := flag.String("cors", "*", "CORS 允许来源（demo 默认 *；空串关闭）")
	redisURL := flag.String("redis-url", os.Getenv("RIICHI_REDIS_URL"),
		"Redis 地址（多实例：共享事件存储 + 跨实例广播；留空 = 单实例内存）")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if *issueToken >= 0 {
		os.Exit(runIssueToken(logger, *secret, *issueToken, *tokenTTL))
	}

	if *secret == "" {
		logger.Warn("未配置 JWT 密钥，使用开发默认值（生产请设置 RIICHI_JWT_SECRET）")
		*secret = devJWTSecret
	}
	cfg := transport.Config{
		JWTSecret:    []byte(*secret),
		AgentTimeout: *agentTimeout,
		Logger:       logger,
		CORSOrigin:   *corsOrigin,
	}
	var redisClient *goredis.Client
	if *redisURL != "" {
		opt, err := goredis.ParseURL(*redisURL)
		if err != nil {
			logger.Error("解析 redis-url 失败", "err", err)
			os.Exit(1)
		}
		redisClient = goredis.NewClient(opt)
		// 多实例必须共享事件存储（观战镜像靠它回放历史）与广播频道。
		cfg.Store = redisstore.New(redisClient)
		cfg.Bus = transport.NewRedisBus(redisClient)
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			logger.Warn("Redis 连接失败（多实例功能不可用）", "err", err)
		}
	}
	server, err := transport.New(cfg)
	if err != nil {
		logger.Error("初始化服务失败", "err", err)
		os.Exit(1)
	}
	if redisClient != nil {
		defer func() { _ = redisClient.Close() }()
	}

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("riichi-server 启动", "addr", *addr, "agent_timeout", *agentTimeout, "cors", *corsOrigin)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("服务异常退出", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	logger.Info("收到退出信号，开始优雅关闭")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP 关闭不干净", "err", err)
	}
	server.Close()
	logger.Info("已退出")
}

// runIssueToken 签发座位令牌并打印，返回进程退出码。
func runIssueToken(logger *slog.Logger, secret string, seat int, ttl time.Duration) int {
	if secret == "" {
		logger.Error("签发令牌需要 -jwt-secret 或 RIICHI_JWT_SECRET")
		return 1
	}
	if seat >= 4 {
		logger.Error("seat 必须在 0..3", "seat", seat)
		return 1
	}
	token, err := transport.SignToken([]byte(secret), game.Seat(seat), ttl)
	if err != nil {
		logger.Error("签发令牌失败", "err", err)
		return 1
	}
	fmt.Println(token)
	return 0
}
