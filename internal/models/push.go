package models

// PushMessage is one push notification: what the phone shows, plus data the
// app reads when it is tapped (for example which competition to open).
type PushMessage struct {
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Data  map[string]string `json:"data"`
}
