package services

import (
	"testing"

	"github.com/google/uuid"
)

func TestGiftReviewTokenRoundTrip(t *testing.T) {
	s := &GiftReviewService{secret: "secret-a"}
	order := uuid.New()

	tok, err := newGiftReviewToken("secret-a", order, ChannelSMS, "+94771234567")
	if err != nil {
		t.Fatal(err)
	}
	claims, got, err := s.parse(tok)
	if err != nil || got != order || claims.Channel != ChannelSMS || claims.Destination != "+94771234567" {
		t.Fatalf("parse = %+v, %v, %v", claims, got, err)
	}

	// A link signed with another key, or a sign-up token, must not open a gift.
	bad, _ := newGiftReviewToken("secret-b", order, ChannelEmail, "a@b.co")
	if _, _, err := s.parse(bad); err == nil {
		t.Error("accepted a token signed with a different secret")
	}
	signup, _ := (&LoginCodeService{jwtSecret: "secret-a"}).signupToken(ChannelEmail, "a@b.co")
	if _, _, err := s.parse(signup); err == nil {
		t.Error("accepted a sign-up token as a review link")
	}
	if _, _, err := s.parse("garbage"); err == nil {
		t.Error("accepted garbage")
	}
}

func TestMaskDestination(t *testing.T) {
	if got := maskDestination(ChannelEmail, "alex@example.com"); got != "a***@example.com" {
		t.Errorf("email mask = %q", got)
	}
	if got := maskDestination(ChannelSMS, "+94771234567"); got != "+947*****567" {
		t.Errorf("phone mask = %q", got)
	}
}
