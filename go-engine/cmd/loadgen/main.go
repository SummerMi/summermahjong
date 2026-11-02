// Command loadgen 是一个最小可用的 HTTP 压测器：固定并发、固定时长，输出 RPS 与分位延迟。
//
// 为什么用 Go 写：Python 的 httpx 客户端在 20 并发左右就会反过来成为瓶颈
// （GIL / 单线程事件循环），测出来的是「客户端上限」而不是「服务端上限」。
// Go 的 goroutine + 独占连接池可以轻松压满单进程 uvicorn。
//
// 用法：
//
//	go run ./cmd/loadgen -base http://127.0.0.1:8000 -path /api/v1/mahjong/tenpai \
//	    -method POST -body '{"hand":["1m","2m","3m","4m","5m","6m","7m","8m","9m","1p","1p","1p","2s"]}' \
//	    -c 100 -d 10s
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8000", "服务地址")
	path := flag.String("path", "/healthz", "请求路径")
	method := flag.String("method", "GET", "HTTP 方法")
	body := flag.String("body", "", "JSON 请求体（POST 用）")
	concurrency := flag.Int("c", 50, "并发数")
	duration := flag.Duration("d", 10*time.Second, "持续时间")
	warmup := flag.Int("warmup", 20, "预热请求数")
	flag.Parse()

	url := strings.TrimRight(*base, "/") + *path

	// 连接池要按并发数放大，否则瓶颈会落在默认的 MaxIdleConnsPerHost=2 上。
	transport := &http.Transport{
		MaxIdleConns:        *concurrency * 2,
		MaxIdleConnsPerHost: *concurrency * 2,
		MaxConnsPerHost:     *concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // 压测不想把 gzip 解压也算进服务端延迟
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	payload := []byte(*body)
	newRequest := func() (*http.Request, error) {
		var reader io.Reader
		if len(payload) > 0 {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequest(*method, url, reader)
		if err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}

	// 预热：连接池建连、路由缓存、运行时预热，避免把冷启动算进结果。
	for i := 0; i < *warmup; i++ {
		req, err := newRequest()
		if err != nil {
			fmt.Println("构造请求失败:", err)
			return
		}
		if resp, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}

	var (
		mu        sync.Mutex
		latencies []time.Duration
		errors    int
		statuses  = map[int]int{}
	)

	deadline := time.Now().Add(*duration)
	var wg sync.WaitGroup
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]time.Duration, 0, 4096)
			localErrors := 0
			localStatus := map[int]int{}
			for time.Now().Before(deadline) {
				req, err := newRequest()
				if err != nil {
					localErrors++
					continue
				}
				start := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					localErrors++
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				local = append(local, time.Since(start))
				localStatus[resp.StatusCode]++
			}
			mu.Lock()
			latencies = append(latencies, local...)
			errors += localErrors
			for code, count := range localStatus {
				statuses[code] += count
			}
			mu.Unlock()
		}()
	}
	start := time.Now()
	wg.Wait()
	elapsed := time.Since(start)

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := percentile(latencies, 0.50)
	p90 := percentile(latencies, 0.90)
	p99 := percentile(latencies, 0.99)
	rps := float64(len(latencies)) / elapsed.Seconds()

	summary := map[string]any{
		"url":         url,
		"concurrency": *concurrency,
		"duration_s":  elapsed.Seconds(),
		"requests":    len(latencies),
		"rps":         rps,
		"p50_ms":      p50,
		"p90_ms":      p90,
		"p99_ms":      p99,
		"errors":      errors,
		"status":      statuses,
	}
	pretty, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(pretty))
}

// percentile 返回 p 分位延迟（毫秒）；输入必须已按升序排好。
func percentile(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return float64(sorted[idx].Microseconds()) / 1000.0
}
