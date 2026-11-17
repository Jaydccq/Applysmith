package captcha

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"applysmith/internal/gates"
)

// TestCaptcha_NoSecretInLogsErrorsOrDetail drives a success, a refused token
// and several failures with canary values and scans every place a value could
// land: the log, the stage detail, the errors, the audit rows and every
// formatting of the solution.
func TestCaptcha_NoSecretInLogsErrorsOrDetail(t *testing.T) {
	canaryKey := "ck" + strings.Repeat("9e", 12)
	canaryToken := "ct" + strings.Repeat("5d", 40)
	canaryPage := "https://canary-page.example.com/apply/" + strings.Repeat("7", 16)

	var log bytes.Buffer
	r, s := newRig(t, func(c *Config) {
		c.Key = func() string { return canaryKey }
		c.Log = slog.New(slog.NewTextHandler(&log, nil))
	})
	r.f.tokens = []string{canaryToken, canaryToken + "b"}
	ch := widget(gates.ChallengeRecaptchaV2)
	ch.PageURL = canaryPage

	var seen []string
	collect := func(vals ...any) {
		for _, v := range vals {
			seen = append(seen, fmt.Sprintf("%v|%+v|%#v|%s|%q", v, v, v, v, v))
		}
	}

	sol, err := s.Solve(ctxT(t), ch)
	if err != nil {
		t.Fatal(err)
	}
	if sol.Token() != canaryToken {
		t.Fatal("the caller must still get the token")
	}
	js, _ := json.Marshal(map[string]any{"s": sol, "c": ch})
	slogLine := &bytes.Buffer{}
	slog.New(slog.NewJSONHandler(slogLine, nil)).Info("x", "sol", sol, "ch", ch)
	collect(sol, ch, &sol, string(js), slogLine.String())

	second, err := s.TokenRefused(ctxT(t), sol)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.TokenRefused(ctxT(t), second)
	collect(err)
	_, err = s.Solve(ctxT(t), ch) // the cap
	collect(err)

	// A service that echoes the key and the token inside its error reply.
	r2, s2 := newRig(t, func(c *Config) {
		c.Key = func() string { return canaryKey }
		c.Log = slog.New(slog.NewTextHandler(&log, nil))
	})
	for _, code := range []string{strings.ToLower(canaryKey), "ERROR_" + canaryToken + " x", canaryKey + "\n"} {
		r2.f.errorCode = strings.NewReplacer(`"`, "", "\n", " ").Replace(code)
		_, err = s2.Solve(ctxT(t), ch)
		collect(err)
		r3, s3 := newRig(t, func(c *Config) {
			c.Key = func() string { return canaryKey }
			c.Log = slog.New(slog.NewTextHandler(&log, nil))
		})
		r3.f.setRaw("/createTask", `{"errorId":0,"junk":"`+canaryKey+canaryToken+`"}`)
		_, err = s3.Solve(ctxT(t), ch)
		collect(err, s3.Detail())
		collect(r3.audited)
	}
	collect(s.Detail(), s2.Detail(), r.audited, r2.audited)

	all := strings.Join(seen, "\n") + "\n" + log.String()
	for name, secret := range map[string]string{"key": canaryKey, "token": canaryToken, "page": "canary-page"} {
		if i := strings.Index(all, secret); i >= 0 {
			t.Logf("context: %q", all[max(0, i-80):min(len(all), i+60)])
			t.Errorf("%s leaked into a log, error, detail, audit row or formatted value", name)
		}
	}
	if log.Len() == 0 {
		t.Fatal("the scan saw no log output, so it proves nothing")
	}
}
