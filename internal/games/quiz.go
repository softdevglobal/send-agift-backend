package games

import (
	"encoding/json"
	"fmt"
)

// QuizSlug identifies the quiz in the catalog.
const QuizSlug = "quiz"

// Quiz scoring: a right answer earns quizBasePoints, plus up to
// quizSpeedPoints more the faster it came, scaled by the time left.
const (
	quizBasePoints  = 100
	quizSpeedPoints = 50
)

// QuizQuestion is one question as the server keeps it, answer included.
type QuizQuestion struct {
	Prompt           string   `json:"prompt"`
	Options          []string `json:"options"`
	CorrectIndex     int      `json:"correct_index"`
	TimeLimitSeconds int      `json:"time_limit_seconds"`
}

// PublicQuizConfig is what a device is given for a quiz play: the prompts,
// options and time limits — never the answers.
func PublicQuizConfig(questions []QuizQuestion, sessionTTLSeconds int) (json.RawMessage, error) {
	type public struct {
		Prompt      string   `json:"prompt"`
		Options     []string `json:"options"`
		TimeLimitMs int      `json:"time_limit_ms"`
	}
	out := struct {
		Questions         []public `json:"questions"`
		SessionTTLSeconds int      `json:"session_ttl_seconds"`
	}{Questions: make([]public, len(questions)), SessionTTLSeconds: sessionTTLSeconds}
	for i, q := range questions {
		out.Questions[i] = public{Prompt: q.Prompt, Options: q.Options, TimeLimitMs: q.TimeLimitSeconds * 1000}
	}
	return json.Marshal(out)
}

// ValidateQuiz checks a round's questions before they are saved.
func ValidateQuiz(questions []QuizQuestion) error {
	if len(questions) < 1 || len(questions) > 50 {
		return fmt.Errorf("a quiz needs 1 to 50 questions")
	}
	for i, q := range questions {
		n := i + 1
		switch {
		case len([]rune(q.Prompt)) < 1 || len([]rune(q.Prompt)) > 500:
			return fmt.Errorf("question %d needs a prompt of 1 to 500 characters", n)
		case len(q.Options) < 2 || len(q.Options) > 6:
			return fmt.Errorf("question %d needs 2 to 6 options", n)
		case q.CorrectIndex < 0 || q.CorrectIndex >= len(q.Options):
			return fmt.Errorf("question %d needs one of its options marked correct", n)
		case q.TimeLimitSeconds < 5 || q.TimeLimitSeconds > 120:
			return fmt.Errorf("question %d needs a time limit of 5 to 120 seconds", n)
		}
		for j, o := range q.Options {
			if len([]rune(o)) < 1 || len([]rune(o)) > 200 {
				return fmt.Errorf("question %d option %d must be 1 to 200 characters", n, j+1)
			}
		}
	}
	return nil
}

// ScoreQuiz scores a quiz play from its answers: one move per question, in
// order, "<question>:<option>:<ms>", where option -1 means the time ran out
// and ms is how long the answer took. A right answer scores 100 plus up to
// 50 for speed. The fastest a person could have played is the time their
// answers took, which the service checks against the real session length.
func ScoreQuiz(questions []QuizQuestion, moves []string) (*Result, error) {
	if len(moves) > len(questions) {
		return nil, fmt.Errorf("%w: %d answers for %d questions", ErrInvalidMove, len(moves), len(questions))
	}
	var score, correct, totalMs int64
	for i, m := range moves {
		f, err := parseQuizMove(m)
		if err != nil {
			return nil, fmt.Errorf("answer %d: %w", i, err)
		}
		q := questions[i]
		limit := int64(q.TimeLimitSeconds) * 1000
		switch {
		case f[0] != i:
			return nil, fmt.Errorf("%w: answer %d is for question %d", ErrInvalidMove, i, f[0])
		case f[1] < -1 || f[1] >= len(q.Options):
			return nil, fmt.Errorf("%w: question %d has no option %d", ErrInvalidMove, i, f[1])
		case f[2] < 0 || int64(f[2]) > limit+1000:
			return nil, fmt.Errorf("%w: question %d took %dms of a %dms limit", ErrInvalidMove, i, f[2], limit)
		}
		ms := int64(f[2])
		if ms > limit {
			ms = limit
		}
		totalMs += ms
		if f[1] == q.CorrectIndex {
			correct++
			score += quizBasePoints + quizSpeedPoints*(limit-ms)/limit
		}
	}
	return &Result{
		Score:         score,
		MovesUsed:     len(moves),
		GameOver:      len(moves) == len(questions),
		MinDurationMs: totalMs * 8 / 10,
		Stats: map[string]int64{
			"correct":   correct,
			"questions": int64(len(questions)),
		},
	}, nil
}

// parseQuizMove reads "<question>:<option>:<ms>"; unlike other logs the
// option may be -1.
func parseQuizMove(m string) ([3]int, error) {
	var f [3]int
	var extra string
	n, _ := fmt.Sscanf(m, "%d:%d:%d%s", &f[0], &f[1], &f[2], &extra)
	if n != 3 {
		return f, fmt.Errorf("%w: %q is not question:option:ms", ErrInvalidMove, m)
	}
	return f, nil
}
