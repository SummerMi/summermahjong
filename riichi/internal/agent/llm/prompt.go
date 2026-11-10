package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// SystemPrompt 是发给 LLM 的系统提示词（OpenAI 兼容客户端与 MockClient 共用语义）。
const SystemPrompt = "你是一名日麻（立直麻将）决策助手。你会收到当前牌局的可观察信息和若干候选切牌" +
	"及其统计（向听/有效进张/危险度/打点潜力）。你只能从候选切牌中选择一张，并且只输出这张牌的编码" +
	"（例如 3p、7s、E、r5m），不要输出解释或其它文字。"

// PromptInput 是 prompt 里的结构化牌局上下文（字段顺序稳定，便于测试与复现）。
//
// 只包含 Observation 里已有的公开信息 + 自己手牌，符合契约 §4「不得依赖未观察信息」。
type PromptInput struct {
	Seat          int           `json:"seat"`
	Hand          []string      `json:"hand"`
	Drawn         string        `json:"drawn,omitempty"`
	Melds         [4][][]string `json:"melds"`
	Discards      [4][]string   `json:"discards"`
	Riichi        [4]bool       `json:"riichi"`
	Scores        [4]int        `json:"scores"`
	WallRemaining int           `json:"wall_remaining"`
	Round         string        `json:"round"`
	Honba         int           `json:"honba"`
	Candidates    []Candidate   `json:"candidates"`
}

// BuildPrompt 把 Observation + top-k 候选拼成一次决策的 user prompt。
//
// prompt 是「结构化 JSON + 明确输出约束」：模型不需要理解规则，只需在候选里挑，
// 这样既降低幻觉，也让离线评测可复现（候选由本地规则计算）。
func BuildPrompt(obs game.Observation, candidates []Candidate) string {
	input := PromptInput{
		Seat:          int(obs.Seat),
		Hand:          tilesToStrings(obs.Hand),
		Melds:         meldStrings(obs.Melds),
		Discards:      discardStrings(obs.Discards),
		Riichi:        obs.Riichi,
		Scores:        obs.Scores,
		WallRemaining: obs.WallRemaining,
		Round:         fmt.Sprintf("%s%d局", obs.RoundWind.String(), obs.Kyoku),
		Honba:         obs.Honba,
		Candidates:    candidates,
	}
	if obs.Drawn != nil {
		input.Drawn = obs.Drawn.String()
	}
	contextJSON, err := json.MarshalIndent(input, "", "  ")
	if err != nil { // 结构体只含基础类型，序列化不会失败；兜底成空对象
		contextJSON = []byte("{}")
	}
	candidatesJSON, err := json.Marshal(candidateCodes(candidates))
	if err != nil {
		candidatesJSON = []byte("[]")
	}
	return fmt.Sprintf(
		"牌局上下文（JSON）：\n%s\n\n候选切牌（按本地综合分排序，只能从中选一张）：\nCANDIDATES: %s\n\n请只输出一张牌的编码（例如 3p 或 r5m），不要加解释。",
		contextJSON, candidatesJSON,
	)
}

// tilesToStrings 把牌切片转成文本数组。
func tilesToStrings(tiles []tile.Tile) []string {
	out := make([]string, len(tiles))
	for i, t := range tiles {
		out[i] = t.String()
	}
	return out
}

// meldStrings 把四家副露转成「每组一个字符串数组」。
func meldStrings(melds [4][]game.Meld) [4][][]string {
	var out [4][][]string
	for seat := 0; seat < 4; seat++ {
		out[seat] = make([][]string, 0, len(melds[seat]))
		for _, meld := range melds[seat] {
			out[seat] = append(out[seat], tilesToStrings(meld.Tiles))
		}
	}
	return out
}

// discardStrings 把四家舍牌转成文本数组。
func discardStrings(discards [4][]tile.Tile) [4][]string {
	var out [4][]string
	for seat := 0; seat < 4; seat++ {
		out[seat] = tilesToStrings(discards[seat])
	}
	return out
}

// candidateCodes 提取候选牌编码（MockClient 与 prompt 的 CANDIDATES 行共用）。
func candidateCodes(candidates []Candidate) []string {
	out := make([]string, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate.Tile
	}
	return out
}

// firstCandidateFromPrompt 从 prompt 的 `CANDIDATES: ["1m", ...]` 行解析第一张候选。
//
// MockClient 用它实现「没有 API key 也能跑通完整决策链路」。
func firstCandidateFromPrompt(prompt string) string {
	const marker = "CANDIDATES: "
	index := strings.LastIndex(prompt, marker)
	if index < 0 {
		return ""
	}
	rest := prompt[index+len(marker):]
	line, _, _ := strings.Cut(rest, "\n")
	var codes []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &codes); err != nil || len(codes) == 0 {
		return ""
	}
	return codes[0]
}
