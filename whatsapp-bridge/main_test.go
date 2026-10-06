package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestParseHistoryBackfillRequest(t *testing.T) {
	validChat := "1234567890@s.whatsapp.net"
	cases := []struct {
		name      string
		body      string
		mediaType string
		wantCount int
		wantError bool
	}{
		{"default count", `{"chat":"` + validChat + `"}`, "application/json", 50, false},
		{"minimum count", `{"chat":"` + validChat + `","count":1}`, "application/json", 1, false},
		{"maximum count", `{"chat":"` + validChat + `","count":50}`, "application/json", 50, false},
		{"zero count", `{"chat":"` + validChat + `","count":0}`, "application/json", 0, true},
		{"negative count", `{"chat":"` + validChat + `","count":-1}`, "application/json", 0, true},
		{"count above cap", `{"chat":"` + validChat + `","count":51}`, "application/json", 0, true},
		{"invalid chat", `{"chat":"not-a-jid","count":10}`, "application/json", 0, true},
		{"unknown field", `{"chat":"` + validChat + `","limit":10}`, "application/json", 0, true},
		{"trailing value", `{"chat":"` + validChat + `"} {}`, "application/json", 0, true},
		{"wrong content type", `{"chat":"` + validChat + `"}`, "text/plain", 0, true},
		{"oversized body", `{"chat":"` + validChat + `"}` + strings.Repeat(" ", 5000), "application/json", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/backfill", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.mediaType)
			w := httptest.NewRecorder()
			jid, count, err := parseHistoryBackfillRequest(w, r)
			if (err != nil) != tc.wantError {
				t.Fatalf("parse error = %v, wantError %t", err, tc.wantError)
			}
			if err == nil && (jid.String() != validChat || count != tc.wantCount) {
				t.Fatalf("parsed chat/count = %q/%d, want %q/%d", jid, count, validChat, tc.wantCount)
			}
		})
	}
}

func TestHistoryBackfillRequiresPost(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/backfill", nil)
	w := httptest.NewRecorder()
	historyBackfillHandler(nil, nil).ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET response = %d, Allow %q; want 405 and POST", w.Code, w.Header().Get("Allow"))
	}
}

func TestLocalBridgeRequestGuard(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		origins    []string
		wantStatus int
	}{
		{"local client without origin", "127.0.0.1:8080", nil, http.StatusNoContent},
		{"same local origin", "localhost:8080", []string{"http://localhost:8080"}, http.StatusNoContent},
		{"rejects DNS rebinding host", "attacker.example:8080", nil, http.StatusForbidden},
		{"rejects cross-origin browser request", "127.0.0.1:8080", []string{"https://attacker.example"}, http.StatusForbidden},
		{"rejects duplicate origins", "localhost:8080", []string{"http://localhost:8080", "https://attacker.example"}, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/send", nil)
			r.Host = tc.host
			for _, origin := range tc.origins {
				r.Header.Add("Origin", origin)
			}
			w := httptest.NewRecorder()
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			localBridgeRequestGuard(8080, next).ServeHTTP(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

func TestMediaStorageNameDoesNotUseRemotePath(t *testing.T) {
	name := mediaStorageName("123@g.us", "message-1", "document", `../../../../etc/passwd`)
	if strings.ContainsAny(name, `/\\`) || name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".bin") {
		t.Fatalf("unsafe storage name %q", name)
	}
}

func TestEnsurePrivateDirectoryRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "store")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ensurePrivateDirectory(link); err == nil {
		t.Fatal("expected symlink store directory to be rejected")
	}
}

func TestOpenPrivateChildDirRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	storePath := filepath.Join(base, "store")
	if err := os.Mkdir(storePath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := openPrivateMediaRoot(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(storePath, "chat")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := openPrivateChildDir(root, "chat"); err == nil {
		t.Fatal("expected symlink chat directory to be rejected")
	}
}

func TestExtractStickerMediaInfo(t *testing.T) {
	sticker := &waProto.Message{StickerMessage: &waProto.StickerMessage{
		URL:           proto.String("https://media.example/sticker.enc"),
		MediaKey:      []byte{1},
		FileSHA256:    []byte{2},
		FileEncSHA256: []byte{3},
		FileLength:    proto.Uint64(4),
	}}
	mediaType, filename, url, mediaKey, sha256, encSHA256, length := extractMediaInfo(sticker)
	if mediaType != "image" || !strings.HasPrefix(filename, "sticker_") || !strings.HasSuffix(filename, ".webp") {
		t.Fatalf("sticker media = (%q, %q), want image sticker_*.webp", mediaType, filename)
	}
	if url != "https://media.example/sticker.enc" || len(mediaKey) != 1 || len(sha256) != 1 || len(encSHA256) != 1 || length != 4 {
		t.Fatalf("sticker download metadata was not preserved")
	}
}

// extractTextContent used to return "" for every non-text message, which made
// StoreMessage drop it (it skips when content and mediaType are both empty)
// while StoreChat had already bumped chats.last_message_time. These cases pin
// the rendered form for each type so that regression cannot return silently.
func TestExtractTextContent(t *testing.T) {
	cases := []struct {
		name string
		msg  *waProto.Message
		want string
	}{
		{"nil message", nil, ""},
		{"empty message", &waProto.Message{}, ""},
		{
			"plain conversation",
			&waProto.Message{Conversation: proto.String("hello")},
			"hello",
		},
		{
			"extended text",
			&waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("quoted reply")}},
			"quoted reply",
		},
		{
			"reaction",
			&waProto.Message{ReactionMessage: &waProto.ReactionMessage{Text: proto.String("❤️")}},
			"[reaction: ❤️]",
		},
		{
			"reaction removed",
			&waProto.Message{ReactionMessage: &waProto.ReactionMessage{Text: proto.String("")}},
			"[reaction removed]",
		},
		{
			"poll with options",
			&waProto.Message{PollCreationMessage: &waProto.PollCreationMessage{
				Name: proto.String("Lunch?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Pizza")},
					{OptionName: proto.String("Sushi")},
				},
			}},
			"[poll: Lunch? — options: Pizza | Sushi]",
		},
		{
			"poll without options",
			&waProto.Message{PollCreationMessage: &waProto.PollCreationMessage{Name: proto.String("Yes or no")}},
			"[poll: Yes or no]",
		},
		{
			"poll vote",
			&waProto.Message{PollUpdateMessage: &waProto.PollUpdateMessage{}},
			"[poll vote]",
		},
		{
			"location named",
			&waProto.Message{LocationMessage: &waProto.LocationMessage{
				Name:             proto.String("Tel Aviv"),
				DegreesLatitude:  proto.Float64(32.085300),
				DegreesLongitude: proto.Float64(34.781800),
			}},
			"[location: Tel Aviv (32.085300, 34.781800)]",
		},
		{
			"location unnamed",
			&waProto.Message{LocationMessage: &waProto.LocationMessage{
				DegreesLatitude:  proto.Float64(1.500000),
				DegreesLongitude: proto.Float64(2.500000),
			}},
			"[location: 1.500000, 2.500000]",
		},
		{
			"live location with caption",
			&waProto.Message{LiveLocationMessage: &waProto.LiveLocationMessage{
				Caption:          proto.String("on my way"),
				DegreesLatitude:  proto.Float64(3.000000),
				DegreesLongitude: proto.Float64(4.000000),
			}},
			"[live location: on my way (3.000000, 4.000000)]",
		},
		{
			"contact card",
			&waProto.Message{ContactMessage: &waProto.ContactMessage{DisplayName: proto.String("Alice")}},
			"[contact: Alice]",
		},
		{
			"contacts array",
			&waProto.Message{ContactsArrayMessage: &waProto.ContactsArrayMessage{
				Contacts: []*waProto.ContactMessage{
					{DisplayName: proto.String("Alice")},
					{DisplayName: proto.String("Bob")},
				},
			}},
			"[contacts: Alice, Bob]",
		},
		{
			"edit recurses into corrected text",
			&waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
				Type:          waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				EditedMessage: &waProto.Message{Conversation: proto.String("corrected text")},
			}},
			"[edited] corrected text",
		},
		{
			"edit with no inner text",
			&waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
			}},
			"[edited message]",
		},
		{
			"revoke",
			&waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_REVOKE.Enum(),
			}},
			"[message deleted]",
		},
		{
			"view once unwraps caption",
			&waProto.Message{ViewOnceMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{Conversation: proto.String("secret")},
			}},
			"[view once] secret",
		},
		{
			"view once v2",
			&waProto.Message{ViewOnceMessageV2: &waProto.FutureProofMessage{
				Message: &waProto.Message{Conversation: proto.String("secret2")},
			}},
			"[view once] secret2",
		},
		{
			"ephemeral unwraps without prefix",
			&waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{Conversation: proto.String("disappearing")},
			}},
			"disappearing",
		},
		{
			"sticker",
			&waProto.Message{StickerMessage: &waProto.StickerMessage{}},
			"[sticker]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractTextContent(tc.msg); got != tc.want {
				t.Errorf("extractTextContent()\n  got:  %q\n  want: %q", got, tc.want)
			}
		})
	}
}

// The bug was that a non-text message produced empty content, which the storage
// gate (content == "" && mediaType == "") then discarded. Assert directly that
// no supported type returns empty.
func TestNonTextMessagesAreNeverEmpty(t *testing.T) {
	msgs := map[string]*waProto.Message{
		"reaction":  {ReactionMessage: &waProto.ReactionMessage{Text: proto.String("👍")}},
		"poll":      {PollCreationMessage: &waProto.PollCreationMessage{Name: proto.String("p")}},
		"poll vote": {PollUpdateMessage: &waProto.PollUpdateMessage{}},
		"location":  {LocationMessage: &waProto.LocationMessage{DegreesLatitude: proto.Float64(1), DegreesLongitude: proto.Float64(2)}},
		"contact":   {ContactMessage: &waProto.ContactMessage{DisplayName: proto.String("x")}},
		"revoke":    {ProtocolMessage: &waProto.ProtocolMessage{Type: waProto.ProtocolMessage_REVOKE.Enum()}},
		"sticker":   {StickerMessage: &waProto.StickerMessage{}},
		"view once": {ViewOnceMessage: &waProto.FutureProofMessage{Message: &waProto.Message{}}},
	}
	for name, m := range msgs {
		if got := extractTextContent(m); got == "" {
			t.Errorf("%s produced empty content — it would be dropped by the storage gate", name)
		}
	}
}

// The production test on 2026-09-06 showed a real poll being dropped: only V1
// was handled while WhatsApp had moved to a later variant. V4 wraps the poll in
// a FutureProofMessage; the others return it directly. Signatures differ
// between whatsmeow releases, so these pin the ones in go.mod.
func TestPollVariants(t *testing.T) {
	p := func() *waProto.PollCreationMessage {
		return &waProto.PollCreationMessage{
			Name:    proto.String("Lunch?"),
			Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("Pizza")}},
		}
	}
	want := "[poll: Lunch? — options: Pizza]"
	cases := map[string]*waProto.Message{
		"V1": {PollCreationMessage: p()},
		"V2": {PollCreationMessageV2: p()},
		"V3": {PollCreationMessageV3: p()},
		"V4": {PollCreationMessageV4: &waProto.FutureProofMessage{Message: &waProto.Message{PollCreationMessage: p()}}},
		"V5": {PollCreationMessageV5: p()},
		"V6": {PollCreationMessageV6: p()},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if got := extractTextContent(m); got != want {
				t.Errorf("poll %s\n  got:  %q\n  want: %q", name, got, want)
			}
		})
	}
}

// An edit can arrive as a top-level EditedMessage rather than inside a
// ProtocolMessage, depending on the sending client.
func TestTopLevelEditedMessage(t *testing.T) {
	m := &waProto.Message{EditedMessage: &waProto.FutureProofMessage{
		Message: &waProto.Message{Conversation: proto.String("corrected")},
	}}
	if got, want := extractTextContent(m), "[edited] corrected"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
