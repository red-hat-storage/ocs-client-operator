package skills_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"maragu.dev/gai/eval"
	"sigs.k8s.io/yaml"
)

const (
	defaultModel        = "gpt-6-luna"
	decisionsModel      = "gpt-6-luna"
	reasoningEffort     = "medium"
	maxOutputTokens     = 4096
	decisionScoreMax    = 2
	saturationThreshold = 0.05
	responsesURL        = "https://api.openai.com/v1/responses"
	decisionsURL        = "https://api.openai.com/v1/decisions"
	baseInstructions    = "Answer concisely. Use general product knowledge for general questions, but claim facts about a specific cluster only when the question provides evidence. If uncertain, say so instead of guessing."
)

type question struct {
	ID       string `json:"id"`
	Polarity string `json:"polarity"`
	Question string `json:"question"`
	Expected string `json:"expected"`
}

type skillEval struct {
	name        string
	instruction string
	questions   []question
}

type decision struct {
	Accuracy          eval.Score
	Coverage          eval.Score
	UnsupportedClaims eval.Score
}

type pairedDecision struct {
	withoutSkill *decision
	withSkill    *decision
}

type saturationSummary struct {
	RecordType             string   `json:"record_type"`
	Skill                  string   `json:"skill"`
	Saturated              *bool    `json:"saturated,omitempty"`
	ComparedQuestions      int      `json:"compared_questions"`
	QuestionCount          int      `json:"question_count"`
	Threshold              float64  `json:"threshold"`
	QualityWithoutSkill    *float64 `json:"quality_without_skill,omitempty"`
	QualityWithSkill       *float64 `json:"quality_with_skill,omitempty"`
	QualityDelta           *float64 `json:"quality_delta,omitempty"`
	AccuracyDelta          *float64 `json:"accuracy_delta,omitempty"`
	CoverageDelta          *float64 `json:"coverage_delta,omitempty"`
	UnsupportedClaimsDelta *float64 `json:"unsupported_claims_delta,omitempty"`
}

type evalRecord struct {
	Name          string
	Group         string
	Sample        eval.Sample
	Results       []eval.Result
	Duration      string
	ResponseUsage *apiUsage `json:"response_usage,omitempty"`
	JudgeUsage    *apiUsage `json:"judge_usage,omitempty"`
}

type apiUsage struct {
	InputTokens         int64              `json:"input_tokens"`
	InputTokensDetails  inputTokenDetails  `json:"input_tokens_details"`
	OutputTokens        int64              `json:"output_tokens"`
	OutputTokensDetails outputTokenDetails `json:"output_tokens_details"`
	TotalTokens         int64              `json:"total_tokens"`
}

type inputTokenDetails struct {
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type outputTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type tokenUsageSummary struct {
	RecordType string   `json:"record_type"`
	Responses  apiUsage `json:"responses"`
	Decisions  apiUsage `json:"decisions"`
}

func (u *apiUsage) add(other *apiUsage) {
	if other == nil {
		return
	}
	u.InputTokens += other.InputTokens
	u.InputTokensDetails.CachedTokens += other.InputTokensDetails.CachedTokens
	u.InputTokensDetails.CacheWriteTokens += other.InputTokensDetails.CacheWriteTokens
	u.OutputTokens += other.OutputTokens
	u.OutputTokensDetails.ReasoningTokens += other.OutputTokensDetails.ReasoningTokens
	u.TotalTokens += other.TotalTokens
}

type openAIClient struct {
	model      string
	apiKey     string
	httpClient *http.Client
}

type decisionsResponse struct {
	Usage   *apiUsage `json:"usage"`
	Answers []struct {
		Type  string  `json:"type"`
		Name  string  `json:"name"`
		Score float64 `json:"score"`
	} `json:"answers"`
}

type responseResult struct {
	Usage             *apiUsage `json:"usage"`
	Status            string    `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func TestEvalSkills(t *testing.T) {
	eval.Run(t, "skills", func(t *testing.T, _ *eval.E) {
		root := skillsDir(t)
		skills := discoverSkillEvals(t, root)
		if len(skills) == 0 {
			t.Fatalf("no skill evals found under %s", root)
		}

		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			t.Fatal("set OPENAI_API_KEY to run the eval")
		}

		client := openAIClient{
			model:      cmp.Or(os.Getenv("OPENAI_MODEL"), defaultModel),
			apiKey:     apiKey,
			httpClient: http.DefaultClient,
		}

		var saturationSummaries []saturationSummary
		var evalRecords []evalRecord
		usage := tokenUsageSummary{RecordType: "token_usage"}
		// Write the collected case results and summaries after all subtests finish.
		t.Cleanup(func() {
			timestamp := time.Now().UTC()
			resultsName := "results-" + timestamp.Format("20060102T150405Z")
			var resultsPath string
			var resultsFile *os.File
			var err error
			for suffix := 0; ; suffix++ {
				filename := resultsName + ".yaml"
				if suffix > 0 {
					filename = fmt.Sprintf("%s-%d.yaml", resultsName, suffix+1)
				}
				resultsPath = filepath.Join(root, filename)
				resultsFile, err = os.OpenFile(resultsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
				if !os.IsExist(err) {
					break
				}
			}
			if err != nil {
				t.Errorf("create eval results: %v", err)
				return
			}

			metadata := map[string]string{
				"record_type":      "run_metadata",
				"timestamp":        timestamp.Format(time.RFC3339),
				"response_model":   client.model,
				"reasoning_effort": reasoningEffort,
				"decision_model":   decisionsModel,
			}
			records := make([]any, 0, len(evalRecords)+len(saturationSummaries)+2)
			records = append(records, metadata)
			for _, record := range evalRecords {
				records = append(records, record)
			}
			records = append(records, usage)
			for _, summary := range saturationSummaries {
				records = append(records, summary)
			}
			results, err := yaml.Marshal(records)
			if err != nil {
				t.Errorf("encode eval results: %v", err)
			} else if _, err := resultsFile.Write(results); err != nil {
				t.Errorf("write eval results: %v", err)
			}
			if err := resultsFile.Close(); err != nil {
				t.Errorf("close eval results: %v", err)
			}
			fmt.Printf("token_usage responses_input=%d responses_output=%d decisions_input=%d decisions_output=%d total=%d\n",
				usage.Responses.InputTokens, usage.Responses.OutputTokens,
				usage.Decisions.InputTokens, usage.Decisions.OutputTokens,
				usage.Responses.TotalTokens+usage.Decisions.TotalTokens,
			)
		})

		for _, skill := range skills {
			scoresByQuestion := make(map[string]pairedDecision, len(skill.questions))
			conditions := []struct {
				name         string
				instructions string
			}{
				{name: "without_skill", instructions: baseInstructions},
				{name: "with_skill", instructions: baseInstructions + "\n\nSkill instructions:\n" + skill.instruction},
			}
			eval.Run(t, skill.name, func(t *testing.T, _ *eval.E) {
				for _, q := range skill.questions {
					for _, condition := range conditions {
						eval.Run(t, condition.name+"/"+q.Polarity+"/"+q.ID, func(t *testing.T, _ *eval.E) {
							started := time.Now()
							answer, responseUsage, err := client.complete(t.Context(), condition.instructions, q.Question)
							usage.Responses.add(responseUsage)
							if err != nil {
								t.Fatal(err)
							}

							sample := eval.NewTextSample(q.Question, q.Expected, answer)
							grade, judgeUsage, err := client.judge(t.Context(), q.Question, q.Expected, answer)
							usage.Decisions.add(judgeUsage)
							if err != nil {
								t.Fatal(err)
							}
							pair := scoresByQuestion[q.ID]
							gradeCopy := grade
							if condition.name == "with_skill" {
								pair.withSkill = &gradeCopy
							} else {
								pair.withoutSkill = &gradeCopy
							}
							scoresByQuestion[q.ID] = pair

							evalRecords = append(evalRecords, evalRecord{
								Name:   t.Name(),
								Group:  "Skills",
								Sample: sample,
								Results: []eval.Result{
									{Type: "JudgeAccuracy", Score: grade.Accuracy},
									{Type: "JudgeCoverage", Score: grade.Coverage},
									{Type: "JudgeUnsupportedClaims", Score: grade.UnsupportedClaims},
								},
								Duration:      time.Since(started).Round(time.Millisecond).String(),
								ResponseUsage: responseUsage,
								JudgeUsage:    judgeUsage,
							})
						})
					}
				}
			})
			summary := summarizeSaturation(skill.name, skill.questions, scoresByQuestion)
			saturationSummaries = append(saturationSummaries, summary)
			status := "incomplete"
			if summary.Saturated != nil {
				status = "not_saturated"
				if *summary.Saturated {
					status = "saturated"
				}
			}
			fmt.Printf("skill=%s saturation=%s threshold=%.3f compared=%d/%d quality_without_skill=%s quality_with_skill=%s quality_delta=%s accuracy_delta=%s coverage_delta=%s unsupported_claims_delta=%s\n",
				summary.Skill, status, summary.Threshold, summary.ComparedQuestions, summary.QuestionCount,
				formatOptionalScore(summary.QualityWithoutSkill), formatOptionalScore(summary.QualityWithSkill),
				formatOptionalScore(summary.QualityDelta), formatOptionalScore(summary.AccuracyDelta),
				formatOptionalScore(summary.CoverageDelta), formatOptionalScore(summary.UnsupportedClaimsDelta),
			)
		}
	})
}

func summarizeSaturation(skillName string, questions []question, scoresByQuestion map[string]pairedDecision) saturationSummary {
	summary := saturationSummary{
		RecordType:    "skill_saturation",
		Skill:         skillName,
		QuestionCount: len(questions),
		Threshold:     saturationThreshold,
	}
	var withoutAccuracy, withAccuracy float64
	var withoutCoverage, withCoverage float64
	var withoutUnsupported, withUnsupported float64
	var withoutQuality, withQuality float64
	for _, question := range questions {
		pair := scoresByQuestion[question.ID]
		if pair.withoutSkill == nil || pair.withSkill == nil {
			continue
		}
		summary.ComparedQuestions++
		withoutAccuracy += float64(pair.withoutSkill.Accuracy)
		withAccuracy += float64(pair.withSkill.Accuracy)
		withoutCoverage += float64(pair.withoutSkill.Coverage)
		withCoverage += float64(pair.withSkill.Coverage)
		withoutUnsupported += float64(pair.withoutSkill.UnsupportedClaims)
		withUnsupported += float64(pair.withSkill.UnsupportedClaims)
		withoutQuality += answerQuality(*pair.withoutSkill)
		withQuality += answerQuality(*pair.withSkill)
	}
	if summary.ComparedQuestions == 0 {
		return summary
	}
	count := float64(summary.ComparedQuestions)
	withoutAccuracy /= count
	withAccuracy /= count
	withoutCoverage /= count
	withCoverage /= count
	withoutUnsupported /= count
	withUnsupported /= count
	withoutQuality /= count
	withQuality /= count

	accuracyDelta := withAccuracy - withoutAccuracy
	coverageDelta := withCoverage - withoutCoverage
	unsupportedDelta := withUnsupported - withoutUnsupported
	qualityDelta := withQuality - withoutQuality
	summary.QualityWithoutSkill = &withoutQuality
	summary.QualityWithSkill = &withQuality
	summary.QualityDelta = &qualityDelta
	summary.AccuracyDelta = &accuracyDelta
	summary.CoverageDelta = &coverageDelta
	summary.UnsupportedClaimsDelta = &unsupportedDelta
	if summary.ComparedQuestions != summary.QuestionCount {
		return summary
	}
	saturated := qualityDelta >= -saturationThreshold && qualityDelta <= saturationThreshold
	summary.Saturated = &saturated
	return summary
}

func answerQuality(grade decision) float64 {
	return (float64(grade.Accuracy) + float64(grade.Coverage) + (1 - float64(grade.UnsupportedClaims))) / 3
}

func formatOptionalScore(score *float64) string {
	if score == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", *score)
}

func (c openAIClient) postJSON(ctx context.Context, apiName, endpoint string, requestBody any) ([]byte, error) {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s API returned %s: %s", apiName, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (c openAIClient) complete(ctx context.Context, instructions, input string) (string, *apiUsage, error) {
	requestBody := map[string]any{
		"model":             c.model,
		"instructions":      instructions,
		"input":             input,
		"reasoning":         map[string]string{"effort": reasoningEffort},
		"store":             false,
		"max_output_tokens": maxOutputTokens,
	}

	body, err := c.postJSON(ctx, "Responses", responsesURL, requestBody)
	if err != nil {
		return "", nil, err
	}

	var result responseResult
	if err := json.Unmarshal(body, &result); err != nil {
		return "", nil, fmt.Errorf("decode Responses API response: %w", err)
	}
	if result.Status != "" && result.Status != "completed" {
		if result.Error != nil && result.Error.Message != "" {
			return "", result.Usage, fmt.Errorf("Responses API returned status %q: %s", result.Status, result.Error.Message)
		}
		if result.IncompleteDetails != nil && result.IncompleteDetails.Reason != "" {
			return "", result.Usage, fmt.Errorf("Responses API returned status %q: %s", result.Status, result.IncompleteDetails.Reason)
		}
		return "", result.Usage, fmt.Errorf("Responses API returned status %q", result.Status)
	}
	var output strings.Builder
	var outputTypes []string
	for _, item := range result.Output {
		outputTypes = append(outputTypes, item.Type)
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" {
				output.WriteString(content.Text)
			}
		}
	}
	if output.Len() == 0 {
		return "", result.Usage, fmt.Errorf("Responses API returned no text output (status=%q, output item types=%v)", result.Status, outputTypes)
	}
	return output.String(), result.Usage, nil
}

func (c openAIClient) judge(ctx context.Context, question, expected, answer string) (decision, *apiUsage, error) {
	input := fmt.Sprintf("Question:\n%s\n\nReference answer:\n%s\n\nCandidate answer:\n%s", question, expected, answer)
	requestBody := map[string]any{
		"model": decisionsModel,
		"input": input,
		"questions": []map[string]any{
			{
				"type":         "score",
				"name":         "accuracy",
				"instructions": "How factually accurate is the candidate answer compared with the reference? Judge claims relevant to the question. Do not penalize different wording.",
				"levels": []map[string]string{
					{"label": "materially inaccurate", "description": "Contains major factual errors or contradicts the reference."},
					{"label": "mostly accurate", "description": "Broadly correct with minor factual errors."},
					{"label": "accurate", "description": "Factual claims are correct and consistent with the reference."},
				},
			},
			{
				"type":         "score",
				"name":         "coverage",
				"instructions": "How much of the material information in the reference does the candidate answer cover?",
				"levels": []map[string]string{
					{"label": "low coverage", "description": "Omits most of the important information needed to answer the question."},
					{"label": "partial coverage", "description": "Covers some important information but omits notable points."},
					{"label": "full coverage", "description": "Covers the important information needed to answer the question."},
				},
			},
			{
				"type":         "score",
				"name":         "unsupported_claims",
				"instructions": "How many or how serious are the candidate answer's claims that the reference does not support? A high score means more unsupported claims.",
				"levels": []map[string]string{
					{"label": "none or negligible", "description": "No material unsupported claims."},
					{"label": "some", "description": "One or more minor unsupported claims."},
					{"label": "many or serious", "description": "Multiple unsupported claims or at least one materially misleading claim."},
				},
			},
		},
	}

	body, err := c.postJSON(ctx, "Decisions", decisionsURL, requestBody)
	if err != nil {
		return decision{}, nil, err
	}

	var result decisionsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return decision{}, nil, fmt.Errorf("decode Decisions API response: %w", err)
	}
	grade := decision{}
	seen := make(map[string]bool, len(result.Answers))
	for _, answer := range result.Answers {
		if answer.Type == "refusal" {
			return decision{}, result.Usage, fmt.Errorf("Decisions API refused judge question %q", answer.Name)
		}
		if answer.Type != "score" {
			return decision{}, result.Usage, fmt.Errorf("Decisions API returned unexpected answer type %q for %q", answer.Type, answer.Name)
		}
		if answer.Score < 0 || answer.Score > decisionScoreMax {
			return decision{}, result.Usage, fmt.Errorf("Decisions API score %s is outside [0,2]: %v", answer.Name, answer.Score)
		}
		seen[answer.Name] = true
		switch answer.Name {
		case "accuracy":
			grade.Accuracy = eval.Score(answer.Score / decisionScoreMax)
		case "coverage":
			grade.Coverage = eval.Score(answer.Score / decisionScoreMax)
		case "unsupported_claims":
			grade.UnsupportedClaims = eval.Score(answer.Score / decisionScoreMax)
		default:
			return decision{}, result.Usage, fmt.Errorf("Decisions API returned unexpected answer name %q", answer.Name)
		}
	}
	for _, name := range []string{"accuracy", "coverage", "unsupported_claims"} {
		if !seen[name] {
			return decision{}, result.Usage, fmt.Errorf("Decisions API omitted judge answer %q", name)
		}
	}
	// Decisions API scores range from 0 to 2. gai/eval requires scores from 0 to 1.
	return grade, result.Usage, nil
}

func skillsDir(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate eval source file")
	}
	return filepath.Dir(source)
}

func discoverSkillEvals(t *testing.T, root string) []skillEval {
	t.Helper()

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	var skills []skillEval
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		evalsPath := filepath.Join(dir, "evals.yaml")
		if _, err := os.Stat(evalsPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat %s: %v", evalsPath, err)
		}

		skillBytes, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			t.Fatalf("read %s/SKILL.md: %v", entry.Name(), err)
		}
		questions := loadEvals(t, evalsPath)
		skills = append(skills, skillEval{
			name:        entry.Name(),
			instruction: string(skillBytes),
			questions:   questions,
		})
	}
	return skills
}

func loadEvals(t *testing.T, path string) []question {
	t.Helper()

	evals, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var questions []question
	if err := yaml.Unmarshal(evals, &questions); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	for i, q := range questions {
		if q.ID == "" || q.Question == "" || q.Expected == "" {
			t.Fatalf("%s case %d is missing id, question, or expected", path, i+1)
		}
		if q.Polarity != "positive" && q.Polarity != "negative" {
			t.Fatalf("%s case %d has invalid polarity %q, want positive or negative", path, i+1, q.Polarity)
		}
	}
	if len(questions) == 0 {
		t.Fatalf("%s contains no questions", path)
	}
	return questions
}
