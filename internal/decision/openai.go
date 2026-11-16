package decision

import (
	"fmt"
	"maps"
	"slices"

	"github.com/SK-ENT/rakitsu/internal/config"
)

type openAIChoice struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

type openAILevel struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type openAIQuestion struct {
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Instructions string         `json:"instructions"`
	Choices      []openAIChoice `json:"choices,omitempty"`
	Levels       []openAILevel  `json:"levels,omitempty"`
}

type openAIDecisionRequest struct {
	Model     string           `json:"model"`
	Input     string           `json:"input"`
	Questions []openAIQuestion `json:"questions"`
}

func openAIRequest(spec *config.DecisionConfig, input string) openAIDecisionRequest {
	questions := make([]openAIQuestion, 0, len(spec.Questions))
	for _, name := range slices.Sorted(maps.Keys(spec.Questions)) {
		q := spec.Questions[name]
		wire := openAIQuestion{Name: name, Type: q.Type, Instructions: q.Instructions}
		for _, value := range slices.Sorted(maps.Keys(q.Choices)) {
			wire.Choices = append(wire.Choices, openAIChoice{Value: value, Description: q.Choices[value]})
		}
		for i, description := range q.Levels {
			wire.Levels = append(wire.Levels, openAILevel{Label: fmt.Sprintf("Level %d", i), Description: description})
		}
		questions = append(questions, wire)
	}
	return openAIDecisionRequest{Model: "gpt-6-luna", Input: input, Questions: questions}
}
