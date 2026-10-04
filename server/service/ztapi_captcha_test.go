package service

import (
	"encoding/base64"
	"image/png"
	"strings"
	"testing"
	"time"
)

func issuedZTAPICaptchaAnswer(t *testing.T, id string) string {
	t.Helper()
	ztAPICaptchaMu.Lock()
	defer ztAPICaptchaMu.Unlock()
	entry, ok := ztAPICaptchaPending[id]
	if !ok {
		t.Fatalf("captcha %q was not stored", id)
	}
	return entry.answer
}

func TestZTAPICaptchaIsReadableAndGradedOnce(t *testing.T) {
	now := time.Now().UTC()
	captcha, err := IssueZTAPICaptcha(now)
	if err != nil {
		t.Fatalf("issue captcha: %v", err)
	}
	if captcha.ID == "" {
		t.Fatal("captcha has no identifier")
	}
	encoded, found := strings.CutPrefix(captcha.ImagePNG, "data:image/png;base64,")
	if !found {
		t.Fatalf("captcha image is not a PNG data URI: %.40s", captcha.ImagePNG)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode captcha image: %v", err)
	}
	image, err := png.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("captcha image does not decode: %v", err)
	}
	if image.Bounds().Dx() != ztAPICaptchaWidth || image.Bounds().Dy() != ztAPICaptchaHeight {
		t.Fatalf("captcha image is %v, want %dx%d", image.Bounds(), ztAPICaptchaWidth, ztAPICaptchaHeight)
	}

	answer := issuedZTAPICaptchaAnswer(t, captcha.ID)
	if len(answer) != ztAPICaptchaLength {
		t.Fatalf("answer %q is not %d characters", answer, ztAPICaptchaLength)
	}
	// People type what they read, in whatever case and with stray spaces.
	if !ConsumeZTAPICaptcha(captcha.ID, " "+strings.ToLower(answer)+" ", now) {
		t.Fatal("the correct answer was refused")
	}
	if ConsumeZTAPICaptcha(captcha.ID, answer, now) {
		t.Fatal("a challenge was accepted twice")
	}
}

func TestZTAPICaptchaRefusesWrongExpiredAndUnknownAnswers(t *testing.T) {
	now := time.Now().UTC()
	captcha, err := IssueZTAPICaptcha(now)
	if err != nil {
		t.Fatalf("issue captcha: %v", err)
	}
	answer := issuedZTAPICaptchaAnswer(t, captcha.ID)

	// One wrong guess burns the challenge, so a code cannot be brute-forced.
	if ConsumeZTAPICaptcha(captcha.ID, answer+"X", now) {
		t.Fatal("a wrong answer was accepted")
	}
	if ConsumeZTAPICaptcha(captcha.ID, answer, now) {
		t.Fatal("a burned challenge was accepted")
	}

	expiring, err := IssueZTAPICaptcha(now)
	if err != nil {
		t.Fatalf("issue captcha: %v", err)
	}
	expiringAnswer := issuedZTAPICaptchaAnswer(t, expiring.ID)
	if ConsumeZTAPICaptcha(expiring.ID, expiringAnswer, now.Add(ztAPICaptchaTTL+time.Second)) {
		t.Fatal("an expired challenge was accepted")
	}

	for _, id := range []string{"", "not-an-issued-id"} {
		if ConsumeZTAPICaptcha(id, "anything", now) {
			t.Fatalf("unknown challenge %q was accepted", id)
		}
	}
}

func TestZTAPICaptchaStoreStaysBounded(t *testing.T) {
	ztAPICaptchaMu.Lock()
	ztAPICaptchaPending = map[string]ztAPICaptchaEntry{}
	ztAPICaptchaMu.Unlock()

	now := time.Now().UTC()
	for count := 0; count < ztAPICaptchaMaxPending+20; count++ {
		storeZTAPICaptcha(string(rune(count))+"-pending", "ABCD", now.Add(ztAPICaptchaTTL), now)
	}
	ztAPICaptchaMu.Lock()
	pending := len(ztAPICaptchaPending)
	ztAPICaptchaMu.Unlock()
	if pending > ztAPICaptchaMaxPending {
		t.Fatalf("%d challenges are pending, want at most %d", pending, ztAPICaptchaMaxPending)
	}
}
