package envelope

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecodeTypedAndValidate(t *testing.T) {
	now := time.Now().UTC()
	e := Envelope{SchemaVersion: 1, MessageID: NewID(), Timestamp: now, From: "a", To: "b", Kind: Ack, Task: "task", Run: "run", Attempt: 1, Fence: 1, TargetSHA: strings.Repeat("a", 40), InReplyTo: "task-message", AckPayload: &AckPayload{State: "ACCEPTED", NextUpdateBy: now.Add(time.Minute)}}
	raw, _ := json.Marshal(e)
	got, err := Decode(raw)
	if err != nil || got.AckPayload.State != "ACCEPTED" {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), []byte("{}")...), []byte(strings.Replace(string(raw), "\"schema_version\":1", "\"schema_version\":2", 1)), []byte(strings.Replace(string(raw), "\"in_reply_to\":\"task-message\",", "", 1))} {
		if _, err := Decode(bad); err == nil {
			t.Fatal("invalid envelope decoded")
		}
	}
	e.HeartbeatPayload = &HeartbeatPayload{}
	if e.Validate() == nil {
		t.Fatal("ambiguous payload accepted")
	}
}
