# go-engine：广东麻将（推倒胡）Go 引擎 + Go/Python 性能对比

同一套牌编码与胡牌 / 听牌语义，Python 版（[`app/services/mahjong/`](../app/services/mahjong/)）与 Go 版（本目录）各实现一遍：

- **语义对齐**：Go 测试直接读 Python 引擎生成的 fixture 做对拍（39 条精选用例 + 2000 条随机手牌）；
- **性能对比**：同一批手牌、同一台机器、单线程，Go `testing.B` 与 Python `timeit` 各跑一遍；
- **只依赖标准库**：`go.mod` 无第三方依赖，`go vet` / `gofmt` 干净。

> 口径提醒：下面的性能数字是「同机、单线程、非压测」的微基准，用于比较**同一算法两种实现**的相对开销，不代表线上 QPS。

---

## 1. 快速开始

在 `go-engine/` 目录下执行：

```bash
# 编译检查 / 静态检查 / 全部测试
go vet ./...
gofmt -l .                 # 无输出即干净
go test ./... -count=1

# 带内存统计的基准（ns/op、B/op、allocs/op）
go test ./mahjong -bench . -benchmem -run '^$'

# 独立可运行的性能演示：逐用例打印 ns/op 与 µs/op
go run ./cmd/bench
```

重新生成 fixture（需要仓库根目录的 `.venv`，会调用 Python 引擎现算期望值）：

```bash
# 在仓库根目录执行
.venv/Scripts/python.exe go-engine/tools/gen_fixtures.py
.venv/Scripts/python.exe go-engine/tools/bench_python.py   # Python 侧 timeit 原始数字
```

环境：Go 1.25.6 / Python 3.12.3 / Windows + AMD Ryzen 7 7745HX。

---

## 2. 目录结构

```
go-engine/
├── go.mod                     # module gd-mahjong-go（无第三方依赖）
├── mahjong/
│   ├── tiles.go               # 牌定义、解析、牌墙、洗牌、发牌、排序、计数、校验
│   ├── rules.go               # WinningPattern / IsWinningHand / WaitingTiles
│   ├── rules_test.go          # 结构性单测 + 读 fixture 的语义对拍
│   └── bench_test.go          # BenchmarkWinningPattern / BenchmarkWaitingTiles
├── cmd/bench/main.go          # 独立性能演示（自动校准迭代次数，打印 µs/op）
├── testdata/
│   ├── cases.json             # 39 条精选用例（Python 生成，含 11 条非法输入）
│   ├── random_cases.json      # 2000 条随机手牌（Python 生成）
│   ├── bench_cases.json       # Go / Python 共用的 11 条 benchmark 输入
│   └── python_bench.json      # Python timeit 原始结果（脚本生成）
├── tools/
│   ├── gen_fixtures.py        # 从 Python 引擎导出 fixture
│   └── bench_python.py        # Python 侧 timeit 对照
└── README.md
```

---

## 3. 语义等价性怎么保证

### 3.1 对齐的语义点

与 `docs/engineering-contract.md` 第 3、4 节、Python 版 `tiles.py` / `rules.py` 逐条对齐：

- 牌编码：`1m..9m`、`1p..9p`、`1s..9s`、`E S W N P F C`，34 种 × 每种 4 张 = 136 张，不含花牌；
- 排序：万 → 筒 → 条 → 东南西北白发中（Go 侧 `Tile` 的数值就是该顺序）；
- 胡牌牌型：标准型（4 面子 + 1 将）与七对（4 张相同算两对）；**两者同时成立时返回 `standard`**；
- 字牌不能组顺子；顺子不能跨花色；
- 听牌：13 张手牌对 34 种牌逐一试加，按牌种顺序升序返回；手牌已有 4 张的牌种不推荐（牌山没有第 5 张）；不听牌返回空列表而不是报错；
- 非法手牌（张数不对 / 未知牌 / 同种超 4 张）→ Go 侧返回可被 `errors.Is(err, mahjong.ErrInvalidHand)` 识别的错误（Python 侧是 `InvalidHandError` 异常）。

### 3.2 fixture 由 Python 真实生成，不是手写

`go-engine/tools/gen_fixtures.py` 直接 import 仓库里的 `app.services.mahjong.rules`，对每条输入调用 `winning_pattern` / `is_winning_hand` / `waiting_tiles` 得到期望值，再写 JSON。
fixture 的 `meta` 里记录了生成脚本、Python 版本、**两个 Python 源文件的 sha256**，方便审阅「这份数据确实来自 Python 实现」：

```json
"source_sha256": {
 "app/services/mahjong/tiles.py": "37077cfeacb0c805ce8dac976edc9c228a4a1d5d7179ca7fb2e106f1816d33f6",
 "app/services/mahjong/rules.py": "37db461c2be77a0696ddb59d14fe7eac79f7fc39d7ba24c61776c9294c6cf567"
}
```

### 3.3 对拍规模

| 文件 | 用例数 | 内容 |
|---|---:|---|
| `testdata/cases.json` | 39 | 标准型 9、七对 3、未胡 5、听牌 11、非法输入 11（含 0/1/12/13/14/15 张、未知牌、同种超 4 张） |
| `testdata/random_cases.json` | 2000 | 610 手胡牌 / 590 手不胡；500 手有听牌 / 300 手不听牌；含纯随机、构造胡牌、构造听牌、七对、改一张牌等 7 类样本 |

Go 测试（`TestCuratedFixture` / `TestRandomCases`）逐条比对 `WinningPattern` / `IsWinningHand` / `WaitingTiles` 的结果；随机用例还会自检正负样本覆盖量，避免「2000 条全是散牌」这种假对拍。
另外每条精选用例的简写文本（如 `"123456789m 123p 99s"`）也会用 Go 的 `ParseHand` 重新解析并比对，顺带覆盖解析器。

### 3.4 明确的「不追求一致」

- **洗牌序列**：Go 用 `math/rand` 局部 Rand + Fisher-Yates，Python 用 `random.Random(seed)`；两者 PRNG 与洗牌实现不同，**同一 seed 不会得到同一副牌墙**。因此对拍只覆盖牌定义、发牌张数、规则判定，不包含洗牌后的具体牌序；两个实现各自保证「同语言、同 seed 可复现」。
- **接口形态**：Python 用异常与就地 `list.pop()`，Go 用 `error` 返回值与「不修改入参、返回剩余牌墙」的切片风格（见第 5 节）。
- 规则本身只做胡牌 / 听牌判定，不含番种、碰杠吃、花牌与赖子。

---

## 4. 性能对比

### 4.1 口径

- 同机、单线程（Go 基准循环单 goroutine；Python 单线程 timeit）、非压测；
- 两边输入是**同一份** `testdata/bench_cases.json`；
- Go：`go test ./mahjong -bench . -benchmem`，数字为 `ns/op`（一次调用，含入参校验），`B/op` / `allocs/op` 为每次操作的堆分配；
- Python：`timeit.Timer.repeat(repeat=7)` 取最小值，再除以调用次数，并**扣掉空 lambda 的调用 / timeit 开销**（本机约 24 ns）；原始数字在 `testdata/python_bench.json`。

### 4.2 结果（14 张胡牌判定）

| 用例 | Go ns/op | Go µs/op | B/op | allocs/op | Python ns/op | Python µs/op | 倍数 |
|---|---:|---:|---:|---:|---:|---:|---:|
| `win_standard` | 87.3 | 0.087 | 0 | 0 | 5715 | 5.71 | 65× |
| `win_backtrack` | 91.5 | 0.091 | 0 | 0 | 4523 | 4.52 | 49× |
| `win_seven_pairs` | 209.2 | 0.209 | 0 | 0 | 12005 | 12.01 | 57× |
| `win_honors` | 76.2 | 0.076 | 0 | 0 | 6249 | 6.25 | 82× |
| `lose_scattered` | 32.1 | 0.032 | 0 | 0 | 3225 | 3.23 | 100× |
| `lose_triplets` | 285.3 | 0.285 | 0 | 0 | 12922 | 12.92 | 45× |

平均约 **67×**（45×–100×）；Go 侧 **0 B/op、0 allocs/op**。

### 4.3 结果（13 张听牌计算，试加 34 种牌）

| 用例 | Go ns/op | Go µs/op | B/op | allocs/op | Python ns/op | Python µs/op | 倍数 |
|---|---:|---:|---:|---:|---:|---:|---:|
| `wait_two_sided` | 2143 | 2.143 | 4 | 1 | 122200 | 122.20 | 57× |
| `wait_nine_gates` | 3538 | 3.538 | 32 | 3 | 164292 | 164.29 | 46× |
| `wait_seven_pairs` | 5515 | 5.515 | 4 | 1 | 262042 | 262.04 | 48× |
| `wait_none` | 534.9 | 0.535 | 4 | 1 | 41821 | 41.82 | 78× |
| `wait_four_copies` | 1893 | 1.893 | 4 | 1 | 112758 | 112.76 | 60× |

平均约 **58×**（46×–78×）。

**怎么读这组数字**（面试时最好主动说）：

1. 胡牌判定 Go 侧 0 allocs/op，说明热点路径完全没碰堆，并发请求不会额外制造 GC 压力；Python 每次调用要建 dict / list / 异常边界，解释器开销主导；
2. 听牌计算 Go 侧只有 1 次小分配（返回的 `[]Tile` 逃逸到堆，`cap=4` 时 4 B/op），算法本身与 Python 相同（34 次试加 + 每面子树搜索）；
3. 倍数是「微基准下的相对值」，两者算法一致，差距主要来自解释执行 vs 编译执行、动态类型 / 装箱、dict 哈希；不要把它包装成线上吞吐结论；
4. 不同用例差距大（散牌 100×、多刻子 45×），因为 Python 的开销里「解释器固定成本」占比高，而复杂分支会让 Go 的递归也变重——这正好说明 benchmark 要按用例看，而不是只报一个平均值。

原始输出见第 7 节；Python 原始数字见 `testdata/python_bench.json`。

---

## 5. Go 的写法为什么和 Python 不像（4 处）

### 5.1 牌用 `Tile uint8` + `[34]uint8` 定长数组，而不是 str + list + dict

Python 版把手牌表示成 `list[str]`，计数用 `list[int]` 或 `dict[str, int]`。Go 版：

```go
type Tile uint8            // 0..33，数值即排序下标
var counts [34]uint8       // 计数直接落在定长数组里
```

好处：牌比较退化成单字节整数比较；没有字符串哈希、没有 `dict` 查表、没有装箱；`[34]uint8` 只有 34 字节，可以整体值拷贝，CPU 缓存友好。
这是「换语言要换数据结构」而不是直译的典型点。

### 5.2 递归拆面子用「值传递的定长数组」，省掉 Python 的减-递归-手工加回

Python 的 `_can_form_melds` 为了省内存，就地改 `counts`，递归返回后再手工恢复：

```python
counts[first] -= 3
if _can_form_melds(counts, melds_left - 1):
    counts[first] += 3      # 成功分支恢复
    return True
counts[first] += 3          # 失败分支也要恢复
```

Go 版直接把数组按值传进递归，每个分支天然拿到独立副本：

```go
func canFormMelds(counts [NumTileKinds]uint8, start, meldsLeft int) bool {
    ...
    next := counts
    next[first] -= 3
    if canFormMelds(next, first, meldsLeft-1) { return true }
```

少了一半还原代码和一类「忘记恢复」的 bug；34 字节的拷贝成本远低于一次分支搜索。另外 Go 版多传一个 `start` 提示：递归只会把「第一个非零下标」往后推，不用每次从 0 重扫前缀。

### 5.3 用返回值代替就地修改（切片是值，不是引用）

Python 的 `draw_tile` 直接 `wall.pop()`，`deal_hands` 就地消耗牌墙。Go 版约定**不修改入参**：

```go
tile, rest, ok := DrawTile(wall)          // 返回剩余牌墙
hands, rest, err := DealHands(wall, nil)  // 发牌后剩余 83 张
shuffled := Shuffle(deck, 42)             // 新切片，原牌墙不变
```

这样调用方不会出现「谁持有同一个可变列表」的别名问题；多个 goroutine 同时读同一副牌墙也安全（只要不写）。代价是调用方要显式接住返回值——Go 里这是更常见的约定。

### 5.4 benchmark 的 `b.ReportAllocs()` / `B/op` / `allocs/op` 不是为了好看

```go
b.ReportAllocs()
for i := 0; i < b.N; i++ {
    pattern, _ = WinningPattern(hand)
}
sinkPattern = pattern   // 包级 sink，防止编译器把循环优化掉
```

- `B/op`：每次操作申请多少堆内存；`allocs/op`：每次操作分配几次。Go 服务里热点函数是否 0 分配直接决定 GC 压力，比 ns/op 更能说明实现质量；
- 只看 ns/op 容易被「少做校验」骗过去，所以两边都跑**完整接口**（含入参校验），并且输入文件共用；
- 基准里必须有 sink，否则编译器可能把「结果没人用」的调用消掉，测出来的是空循环；
- `go test -bench` 的 `-16` 后缀表示 `GOMAXPROCS`，本基准是单 goroutine 串行调用，不是并发压测。

### 5.5 错误处理：`error` 返回值 + `errors.Is`，而不是异常

```go
var (
    ErrMahjong        = errors.New("mahjong: 领域错误")
    ErrInvalidHand    = errors.New("mahjong: 手牌非法")
    ErrNotEnoughTiles = errors.New("mahjong: 牌墙张数不足")
)
```

`InvalidHandError` / `NotEnoughTilesError` 是带原因的具体类型，`Is` 方法让 `errors.Is(err, ErrInvalidHand)` 与 `errors.Is(err, ErrMahjong)` 同时成立。API 层照旧可以把 `ErrInvalidHand` 映射成 HTTP 400，与 Python 侧 `InvalidHandError(ValueError)` 的用法对应。

---

## 6. 面试官可能追问的 3 个 Go 点

1. **「为什么 `WinningPattern` 是 0 allocs/op？编译器帮你做了什么？」**
   `counts` 是局部定长数组，`ValidateHand` 的变长参数 `[]int{14}` 被内联后没有逃逸，返回值是 `Pattern`（字符串常量），整条路径没有堆分配。
   可以现场用 `go build -gcflags='-m'`（或 `-m=2`）看逃逸分析，说明「不是手工省出来的，是让编译器能证明不逃逸」。
2. **「为什么不用 `map[Tile]int` 计数，定长数组不是浪费吗？」**
   34 × 1 字节 = 34 字节，比 map 的 header + bucket 小得多；数组按下标直接寻址、无哈希、无随机迭代顺序，且「从最小非零牌开始拆面子」天然就是顺序扫描。Go 里小规模稠密键用定长数组几乎是默认选择。
3. **「听牌函数还有 1 次分配，能优化吗？递归会不会栈溢出？」**
   能：`WaitingTiles` 现在 `make([]Tile, 0, 4)` 并返回，切片逃逸到堆；如果调用方愿意传 buffer（或返回 `[34]bool` / 计数），可以做到 0 allocs，但会牺牲接口易用性，属于「按调用方需求再优化」的取舍。
   递归深度最多 4 层面子 + 1 层将牌（14 张牌），分支只出现在「同一张牌可当刻子也可当顺子」的少数情况，实测 `lose_triplets` 这类最坏用例也只有 285 ns，不存在栈溢出风险；真要再快可以加「剩余张数模 3」这类剪枝，但那会偏离与 Python 版逐行可对照的算法结构。

---

## 7. 验证命令与真实输出

以下为本机（2026-11-02）实跑输出，命令在 `go-engine/` 下执行。

```
$ go vet ./...
(无输出 = 通过)

$ gofmt -l .
(无输出 = 通过)

$ go test ./... -count=1
?   	gd-mahjong-go/cmd/bench	[no test files]
ok  	gd-mahjong-go/mahjong	0.579s
```

```
$ go test ./mahjong -bench . -benchmem -run '^$'
goos: windows
goarch: amd64
pkg: gd-mahjong-go/mahjong
cpu: AMD Ryzen 7 7745HX with Radeon Graphics
BenchmarkWinningPattern/win_standard-16         	13364606	        87.28 ns/op	       0 B/op	       0 allocs/op
BenchmarkWinningPattern/win_backtrack-16        	12874596	        91.50 ns/op	       0 B/op	       0 allocs/op
BenchmarkWinningPattern/win_seven_pairs-16      	 5481043	       209.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkWinningPattern/win_honors-16           	16098824	        76.24 ns/op	       0 B/op	       0 allocs/op
BenchmarkWinningPattern/lose_scattered-16       	39577836	        32.15 ns/op	       0 B/op	       0 allocs/op
BenchmarkWinningPattern/lose_triplets-16        	 4260325	       285.3 ns/op	       0 B/op	       0 allocs/op
BenchmarkWaitingTiles/wait_two_sided-16         	  554892	      2143 ns/op	       4 B/op	       1 allocs/op
BenchmarkWaitingTiles/wait_nine_gates-16        	  337432	      3538 ns/op	      32 B/op	       3 allocs/op
BenchmarkWaitingTiles/wait_seven_pairs-16       	  251614	      5515 ns/op	       4 B/op	       1 allocs/op
BenchmarkWaitingTiles/wait_none-16              	 2183430	       534.9 ns/op	       4 B/op	       1 allocs/op
BenchmarkWaitingTiles/wait_four_copies-16       	  655293	      1893 ns/op	       4 B/op	       1 allocs/op
PASS
ok  	gd-mahjong-go/mahjong	15.419s
```

Python 侧同批用例（`tools/bench_python.py`，已扣掉约 24 ns 调用开销）：

```
| case             |   ns/op |  µs/op |
| ---------------- | ------: | -----: |
| win_standard     |  5714.8 |  5.715 |
| win_backtrack    |  4522.7 |  4.523 |
| win_seven_pairs  | 12005.2 | 12.005 |
| win_honors       |  6248.8 |  6.249 |
| lose_scattered   |  3224.9 |  3.225 |
| lose_triplets    | 12921.6 | 12.922 |
| wait_two_sided   | 122200  | 122.20 |
| wait_nine_gates  | 164292  | 164.29 |
| wait_seven_pairs | 262042  | 262.04 |
| wait_none        | 41821   |  41.82 |
| wait_four_copies | 112758  | 112.76 |
```

测试统计：15 个 Go 测试函数（含 39 条精选用例子测试 + 2000 条随机对拍），全绿；`go vet` / `gofmt` 无输出。
（基准数字会随 CPU 频率 / 后台负载小幅波动，量级与倍数关系稳定。）

---

## 8. 局限与后续

- 只做胡牌牌型与听牌判定：不含番种计算、碰杠吃、花牌 / 赖子、多家结算；
- 洗牌跨语言不可复现（见 3.4），若需要「同一 seed 双语言同一牌墙」，得在 Go 里实现 Python 的 MT19937 与 `shuffle` 算法，成本大于收益；
- 微基准不是压测：真实服务里还要看请求序列化、路由、锁竞争；如果后续把引擎接进 HTTP 层，可用 `go test -bench` + `pprof` 再看分配热点。
