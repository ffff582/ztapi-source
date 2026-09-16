package service

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
)

// Registration is open to anyone, so it is gated by a challenge this server
// generates and grades itself: no third-party script to load, nothing for a
// visitor behind a slow or blocked CDN to wait for.
const (
	ztAPICaptchaLength     = 4
	ztAPICaptchaTTL        = 3 * time.Minute
	ztAPICaptchaMaxPending = 4096
	ztAPICaptchaWidth      = 132
	ztAPICaptchaHeight     = 48
	// Characters a person cannot confuse with one another.
	ztAPICaptchaAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
)

var ErrZTAPICaptchaUnavailable = errors.New("captcha could not be generated")

type ztAPICaptchaEntry struct {
	answer    string
	expiresAt time.Time
}

var (
	ztAPICaptchaMu      sync.Mutex
	ztAPICaptchaPending = map[string]ztAPICaptchaEntry{}
)

// ZTAPICaptcha is one challenge: the identifier the client sends back and the
// PNG a person reads. The answer never leaves the server.
type ZTAPICaptcha struct {
	ID       string `json:"captcha_id"`
	ImagePNG string `json:"captcha_image"`
}

func IssueZTAPICaptcha(now time.Time) (ZTAPICaptcha, error) {
	answer, err := randomZTAPICaptchaAnswer()
	if err != nil {
		return ZTAPICaptcha{}, ErrZTAPICaptchaUnavailable
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return ZTAPICaptcha{}, ErrZTAPICaptchaUnavailable
	}
	image, err := drawZTAPICaptcha(answer)
	if err != nil {
		return ZTAPICaptcha{}, ErrZTAPICaptchaUnavailable
	}
	id := hex.EncodeToString(identifier)
	storeZTAPICaptcha(id, answer, now.Add(ztAPICaptchaTTL), now)
	return ZTAPICaptcha{ID: id, ImagePNG: image}, nil
}

// ConsumeZTAPICaptcha grades one attempt and discards the challenge either
// way, so a single image can never be guessed at twice.
func ConsumeZTAPICaptcha(id, answer string, now time.Time) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	ztAPICaptchaMu.Lock()
	entry, found := ztAPICaptchaPending[id]
	delete(ztAPICaptchaPending, id)
	ztAPICaptchaMu.Unlock()
	if !found || now.After(entry.expiresAt) {
		return false
	}
	submitted := strings.ToUpper(strings.TrimSpace(answer))
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(entry.answer)) == 1
}

func storeZTAPICaptcha(id, answer string, expiresAt, now time.Time) {
	ztAPICaptchaMu.Lock()
	defer ztAPICaptchaMu.Unlock()
	if len(ztAPICaptchaPending) >= ztAPICaptchaMaxPending {
		for key, entry := range ztAPICaptchaPending {
			if now.After(entry.expiresAt) {
				delete(ztAPICaptchaPending, key)
			}
		}
	}
	// A flood of unanswered challenges must not grow without bound, so the
	// oldest pending one makes way once the table is still full.
	for len(ztAPICaptchaPending) >= ztAPICaptchaMaxPending {
		oldestKey, oldest := "", time.Time{}
		for key, entry := range ztAPICaptchaPending {
			if oldestKey == "" || entry.expiresAt.Before(oldest) {
				oldestKey, oldest = key, entry.expiresAt
			}
		}
		delete(ztAPICaptchaPending, oldestKey)
	}
	ztAPICaptchaPending[id] = ztAPICaptchaEntry{answer: answer, expiresAt: expiresAt}
}

func randomZTAPICaptchaAnswer() (string, error) {
	buffer := make([]byte, ztAPICaptchaLength)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	answer := make([]byte, ztAPICaptchaLength)
	for index, value := range buffer {
		answer[index] = ztAPICaptchaAlphabet[int(value)%len(ztAPICaptchaAlphabet)]
	}
	return string(answer), nil
}

func randomZTAPICaptchaInt(limit int) int {
	if limit <= 0 {
		return 0
	}
	var value uint16
	if err := binary.Read(rand.Reader, binary.BigEndian, &value); err != nil {
		return 0
	}
	return int(value) % limit
}

func drawZTAPICaptcha(answer string) (string, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, ztAPICaptchaWidth, ztAPICaptchaHeight))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{R: 244, G: 246, B: 248, A: 255}}, image.Point{}, draw.Src)
	drawZTAPICaptchaNoise(canvas)

	// Each character is drawn about its own centre, so rotation and scale keep
	// it inside the image instead of pushing it off an edge.
	step := float64(ztAPICaptchaWidth) / float64(len(answer)+1)
	for index, character := range answer {
		glyph := renderZTAPICaptchaGlyph(character)
		width := float64(glyph.Bounds().Dx())
		height := float64(glyph.Bounds().Dy())
		angle := (float64(randomZTAPICaptchaInt(41)) - 20) / 100
		scale := 2.7 + float64(randomZTAPICaptchaInt(50))/100
		sin, cos := math.Sin(angle), math.Cos(angle)
		a, b := scale*cos, -scale*sin
		c, d := scale*sin, scale*cos
		centreX := step*(float64(index)+1) + float64(randomZTAPICaptchaInt(7)) - 3
		centreY := float64(ztAPICaptchaHeight)/2 + float64(randomZTAPICaptchaInt(7)) - 3
		transform := f64.Aff3{
			a, b, centreX - (a*width/2 + b*height/2),
			c, d, centreY - (c*width/2 + d*height/2),
		}
		draw.ApproxBiLinear.Transform(canvas, transform, glyph, glyph.Bounds(), draw.Over, nil)
	}

	buffer := &bytes.Buffer{}
	if err := png.Encode(buffer, canvas); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes()), nil
}

// renderZTAPICaptchaGlyph draws one character with the bundled bitmap font, so
// the image needs no font file on disk.
func renderZTAPICaptchaGlyph(character rune) *image.RGBA {
	face := basicfont.Face7x13
	glyph := image.NewRGBA(image.Rect(0, 0, face.Advance, face.Height))
	ink := color.RGBA{
		R: uint8(20 + randomZTAPICaptchaInt(70)),
		G: uint8(20 + randomZTAPICaptchaInt(70)),
		B: uint8(60 + randomZTAPICaptchaInt(90)),
		A: 255,
	}
	drawer := &font.Drawer{
		Dst:  glyph,
		Src:  &image.Uniform{ink},
		Face: face,
		Dot:  fixed.P(0, face.Ascent),
	}
	drawer.DrawString(string(character))
	return glyph
}

func drawZTAPICaptchaNoise(canvas *image.RGBA) {
	for count := 0; count < 3; count++ {
		fromX, fromY := randomZTAPICaptchaInt(ztAPICaptchaWidth), randomZTAPICaptchaInt(ztAPICaptchaHeight)
		toX, toY := randomZTAPICaptchaInt(ztAPICaptchaWidth), randomZTAPICaptchaInt(ztAPICaptchaHeight)
		line := color.RGBA{
			R: uint8(120 + randomZTAPICaptchaInt(90)),
			G: uint8(120 + randomZTAPICaptchaInt(90)),
			B: uint8(120 + randomZTAPICaptchaInt(90)),
			A: 255,
		}
		steps := ztAPICaptchaWidth
		for step := 0; step <= steps; step++ {
			progress := float64(step) / float64(steps)
			x := int(float64(fromX) + progress*float64(toX-fromX))
			y := int(float64(fromY) + progress*float64(toY-fromY))
			canvas.Set(x, y, line)
			canvas.Set(x, y+1, line)
		}
	}
	for count := 0; count < 120; count++ {
		canvas.Set(randomZTAPICaptchaInt(ztAPICaptchaWidth), randomZTAPICaptchaInt(ztAPICaptchaHeight), color.RGBA{
			R: uint8(150 + randomZTAPICaptchaInt(80)),
			G: uint8(150 + randomZTAPICaptchaInt(80)),
			B: uint8(150 + randomZTAPICaptchaInt(80)),
			A: 255,
		})
	}
}
