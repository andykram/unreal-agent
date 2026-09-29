package repl

import (
	"encoding/json/v2"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type skillMatch struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
}

type skillSearchResult struct {
	Query   string       `json:"query"`
	Matches []skillMatch `json:"matches"`
}

func skillSearchDefinition() tool.Definition {
	return tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: "SkillSearch", Description: "Find relevant available skills using local TF-IDF cosine similarity. Describe the task or skill you need. Returns up to five ranked names and descriptions. Then call SkillUse with a returned name to load its instructions.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": "Task description or skill name to search for.", "minLength": 1, "maxLength": 2000}}, "required": []string{"query"}, "additionalProperties": false}}}
}

// Word features capture vocabulary overlap; character trigrams tolerate partial
// names and typos. This is lexical similarity, not an embedding model.
func skillFeatures(text string) map[string]float64 {
	result := map[string]float64{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		switch word {
		case "a", "an", "the", "to", "of", "and", "or", "for", "in", "on", "with", "is", "it", "use", "when", "this", "that":
			continue
		}
		result["w:"+word]++
		runes := []rune(word)
		for i := 0; i+3 <= len(runes); i++ {
			result["g:"+string(runes[i:i+3])] += .2
		}
	}
	return result
}

func similarSkills(skills []tool.Skill, query string) []skillMatch {
	documents := make([]map[string]float64, len(skills))
	frequency := map[string]int{}
	for i, skill := range skills {
		documents[i] = skillFeatures(skill.Name + " " + skill.Name + " " + skill.Description)
		for term := range documents[i] {
			frequency[term]++
		}
	}
	vector := skillFeatures(query)
	idf := func(term string) float64 { return 1 + math.Log(float64(len(skills)+1)/float64(frequency[term]+1)) }
	norm := func(features map[string]float64) float64 {
		sum := 0.0
		for term, value := range features {
			v := value * idf(term)
			sum += v * v
		}
		return math.Sqrt(sum)
	}
	queryNorm := norm(vector)
	matches := []skillMatch{}
	if queryNorm == 0 {
		return matches
	}
	for i, features := range documents {
		dot := 0.0
		for term, value := range vector {
			weight := idf(term)
			dot += value * features[term] * weight * weight
		}
		denominator := queryNorm * norm(features)
		if dot <= 0 || denominator == 0 {
			continue
		}
		score := math.Round(dot/denominator*10000) / 10000
		matches = append(matches, skillMatch{Name: skills[i].Name, Description: skills[i].Description, Score: score})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].Name < matches[j].Name
		}
		return matches[i].Score > matches[j].Score
	})
	if len(matches) > 5 {
		matches = matches[:5]
	}
	return matches
}

type skillSearchTranslator struct{ registry *skillRegistry }

func (translator skillSearchTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	var arguments struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &arguments, json.RejectUnknownMembers(true)); err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	arguments.Query = strings.TrimSpace(arguments.Query)
	if arguments.Query == "" || len([]rune(arguments.Query)) > 2000 {
		return tool.ErrorStatus("query must contain 1–2000 characters", operation.DefaultMaxOutputLength)
	}
	translator.registry.mu.RLock()
	var skills []tool.Skill
	for _, skill := range translator.registry.Registry.Skills() {
		if translator.registry.allowed[skill.Name] {
			skills = append(skills, skill)
		}
	}
	translator.registry.mu.RUnlock()
	result := skillSearchResult{Query: arguments.Query, Matches: similarSkills(skills, arguments.Query)}
	data, err := json.Marshal(result)
	if err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	spec, err := operation.NewValueSpec(data)
	if err != nil {
		return tool.ErrorStatus(err.Error(), operation.DefaultMaxOutputLength)
	}
	spec.MaxOutputLength = operation.MaxOutputLength
	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}
func (skillSearchTranslator) TranslateResult(id string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	text := status.Error
	if text == "" {
		if len(operations) != 1 {
			return llm.ToolResult{}, fmt.Errorf("skill search needs one operation")
		}
		value, err := operation.DecodeValue(operations[0])
		if err != nil {
			return llm.ToolResult{}, err
		}
		if operations[0].Status == operation.StatusCompleted {
			text = string(value)
		} else {
			text = "Skill search: " + string(operations[0].Status)
		}

	}
	return llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}, nil
}
