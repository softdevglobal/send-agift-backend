package games

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var testQuiz = []QuizQuestion{
	{Prompt: "2 + 2?", Options: []string{"3", "4"}, CorrectIndex: 1, TimeLimitSeconds: 10},
	{Prompt: "Capital of NZ?", Options: []string{"Auckland", "Wellington", "Christchurch"}, CorrectIndex: 1, TimeLimitSeconds: 20},
}

func TestScoreQuizRewardsRightAndFast(t *testing.T) {
	// Q1 right at half time: 100 + 25. Q2 wrong: 0.
	res, err := ScoreQuiz(testQuiz, []string{"0:1:5000", "1:0:1000"})
	if err != nil || res.Score != 125 || res.Stats["correct"] != 1 || !res.GameOver {
		t.Fatalf("got %+v, %v", res, err)
	}
	if res.MinDurationMs != 4800 {
		t.Fatalf("min duration %d", res.MinDurationMs)
	}
	// Instant right answers score the full 150 each.
	res, _ = ScoreQuiz(testQuiz, []string{"0:1:0", "1:1:0"})
	if res.Score != 300 {
		t.Fatalf("perfect quiz scored %d", res.Score)
	}
	// Running out of time scores nothing.
	res, _ = ScoreQuiz(testQuiz, []string{"0:-1:10000"})
	if res.Score != 0 || res.GameOver {
		t.Fatalf("timed out: %+v", res)
	}
}

func TestScoreQuizRejectsBadLogs(t *testing.T) {
	for _, moves := range [][]string{
		{"1:1:100"},                       // out of order
		{"0:5:100"},                       // no such option
		{"0:1:99999"},                     // longer than the limit allows
		{"0:1:100", "1:1:100", "2:0:100"}, // more answers than questions
		{"0:1"},                           // malformed
	} {
		if _, err := ScoreQuiz(testQuiz, moves); !errors.Is(err, ErrInvalidMove) {
			t.Errorf("%v: want ErrInvalidMove, got %v", moves, err)
		}
	}
}

func TestPublicQuizConfigHidesTheAnswers(t *testing.T) {
	raw, err := PublicQuizConfig(testQuiz, 1800)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "correct") {
		t.Fatalf("the answers leaked: %s", raw)
	}
	var cfg struct {
		Questions []struct {
			TimeLimitMs int `json:"time_limit_ms"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg.Questions) != 2 || cfg.Questions[1].TimeLimitMs != 20000 {
		t.Fatalf("config %s: %v", raw, err)
	}
}

func TestValidateQuiz(t *testing.T) {
	if err := ValidateQuiz(testQuiz); err != nil {
		t.Fatal(err)
	}
	bad := []QuizQuestion{{Prompt: "x", Options: []string{"a", "b"}, CorrectIndex: 2, TimeLimitSeconds: 10}}
	if err := ValidateQuiz(bad); err == nil {
		t.Fatal("a correct answer outside the options must be refused")
	}
	if err := ValidateQuiz(nil); err == nil {
		t.Fatal("an empty quiz must be refused")
	}
}
