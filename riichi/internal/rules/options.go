package rules

// Options 是规则计算的可选语义开关。
//
// 目前只有一个开关：七对子的判定口径。
//   - 零值（SevenPairsStrict == nil）是**日麻严格语义**：七对子必须是 7 种不同的对子，
//     4 张相同只算 1 对，不能拆成两对；
//   - SevenPairsStrict 显式指向 false 时切到 **v1 兼容语义（中国规则）**：
//     4 张相同算两对，用于让 fastapi/go-engine 的 v1 金标 fixture 继续逐条对拍。
//
// 用 *bool 而不是 bool，是为了让「零值 = 默认严格」成立（Go 的 bool 零值是 false，
// 若直接声明 bool 字段，默认会变成兼容模式）。生产代码通常直接使用 StrictOptions()；
// 只有 v1 回归对拍才用 CompatOptions()。
type Options struct {
	// SevenPairsStrict 控制七对语义：nil 或 true = 日麻严格；false = v1 兼容（4 张算两对）。
	SevenPairsStrict *bool
}

// StrictSevenPairs 报告是否使用日麻严格七对语义（默认行为）。
func (o Options) StrictSevenPairs() bool {
	return o.SevenPairsStrict == nil || *o.SevenPairsStrict
}

// StrictOptions 返回默认的日麻严格语义选项。
func StrictOptions() Options {
	return Options{}
}

// CompatOptions 返回 v1（中国规则）兼容语义：七对里 4 张相同算两对。
func CompatOptions() Options {
	strict := false
	return Options{SevenPairsStrict: &strict}
}
