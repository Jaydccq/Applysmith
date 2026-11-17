package telegram

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/vault"
)

func TestTelegram_Setup_GetMeValidatesAndRejectsWithoutEcho(t *testing.T) {
	r := newRig(t)
	name, err := r.mgr.SetToken(bg, fakeToken())
	if err != nil || name != "ExampleBot" {
		t.Fatalf("set = %q %v", name, err)
	}
	if r.state.Bot(bg) != "ExampleBot" {
		t.Error("the bot's username was not recorded")
	}
	if got, err := r.vault.Get(TokenSecret); err != nil || got != fakeToken() {
		t.Errorf("the token is not in the keychain: %v", err)
	}

	// Telegram refuses a token of the right shape.
	r2 := newRig(t)
	other := "987654321:" + strings.Repeat("Zz_9-", 7)
	_, err = r2.mgr.SetToken(bg, other)
	if err == nil || !strings.Contains(err.Error(), "auth") || strings.Contains(err.Error(), other) || strings.Contains(err.Error(), "987654321") {
		t.Fatalf("error = %v", err)
	}
	if _, gerr := r2.vault.Get(TokenSecret); !vault.IsCode(gerr, vault.CodeNotFound) {
		t.Errorf("a refused token was stored: %v", gerr)
	}
	// A string that is not a token is refused before any request.
	if _, err := r2.mgr.SetToken(bg, "hello world"); err != ErrTokenShape || strings.Contains(err.Error(), "hello") {
		t.Errorf("shape error = %v", err)
	}
}

func TestTelegram_Setup_TokenOnlyInKeychainEnvOverrides(t *testing.T) {
	r := newRig(t)
	if _, err := r.mgr.SetToken(bg, fakeToken()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.mgr.BeginPairing(bg); err != nil {
		t.Fatal(err)
	}
	// Neither the database nor its write-ahead log holds the token.
	r.st.Checkpoint()
	for _, name := range []string{r.dbPath, r.dbPath + "-wal"} {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), fakeToken()) || strings.Contains(string(b), strings.Split(fakeToken(), ":")[1]) {
			t.Errorf("%s holds the token", filepath.Base(name))
		}
	}
	// The environment overrides the keychain for headless hosts.
	envTok := "555555555:" + strings.Repeat("Ev_1-", 7)
	v, err := vault.New("ws-test", vault.NewMemory(), vault.WithGetenv(func(k string) string {
		if k == vault.EnvName(TokenSecret) {
			return envTok
		}
		return ""
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := TokenSource(v)(); err != nil || got != envTok {
		t.Errorf("env override = %q %v", got, err)
	}
}

func TestTelegram_Setup_SetAndClearTokenStartStopsPoller(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Pause = time.Millisecond })
	r.run()
	if r.gw.Health() != "stopped" {
		t.Fatalf("health with no token = %s", r.gw.Health())
	}
	if _, err := r.mgr.SetToken(bg, fakeToken()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "polling to start", func() bool { r.bot.mu.Lock(); defer r.bot.mu.Unlock(); return r.bot.polls > 0 })
	eventually(t, "a healthy poller", func() bool { return r.gw.Health() == "healthy" })
	if err := r.mgr.ClearToken(bg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "polling to stop", func() bool { return r.gw.Health() == "stopped" })
	if r.state.Bot(bg) != "" {
		t.Error("the bot's name outlived its token")
	}
	if _, err := r.vault.Get(TokenSecret); !vault.IsCode(err, vault.CodeNotFound) {
		t.Errorf("the keychain entry remains: %v", err)
	}
}

func TestTelegram_Pairing_CodeShapeHashOnlyTenMinutes(t *testing.T) {
	r := newRig(t)
	code, err := r.state.BeginPairing(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(code.Code) != 10 || strings.Trim(code.Code, "0123456789abcdef") != "" {
		t.Errorf("code = %q", code.Code)
	}
	if code.ExpiresAt.Sub(r.clk.Now()) != 10*time.Minute {
		t.Errorf("valid for %v", code.ExpiresAt.Sub(r.clk.Now()))
	}
	var hash string
	if err := r.st.Read().QueryRow(`SELECT code_hash FROM telegram_pairing`).Scan(&hash); err != nil || hash == code.Code || len(hash) != 64 {
		t.Errorf("stored %q %v", hash, err)
	}
	other, _ := r.state.BeginPairing(bg)
	if other.Code == code.Code {
		t.Error("two codes were equal")
	}
}

func TestTelegram_Pairing_ClaimBindsChatAndUserConsumesCode(t *testing.T) {
	r := newRig(t)
	code, _ := r.state.BeginPairing(bg)
	res, err := r.state.Claim(bg, ownerChat, ownerUser, code.Code)
	if err != nil || res != Claimed {
		t.Fatalf("claim = %v %v", res, err)
	}
	o, ok := r.state.Owner(bg)
	if !ok || o.ChatID != ownerChat || o.UserID != ownerUser {
		t.Errorf("owner = %+v", o)
	}
	if res, _ := r.state.Claim(bg, 5, 6, code.Code); res == Claimed {
		t.Error("a code was used twice")
	}
}

func TestTelegram_Pairing_ExpiredCodeBindsNothing(t *testing.T) {
	r := newRig(t)
	code, _ := r.state.BeginPairing(bg)
	r.clk.advance(11 * time.Minute)
	if res, _ := r.state.Claim(bg, ownerChat, ownerUser, code.Code); res == Claimed {
		t.Fatal("claimed after 11 minutes")
	}
	if _, ok := r.state.Owner(bg); ok {
		t.Error("an owner was recorded")
	}
}

func TestTelegram_Pairing_FiveWrongCodesCloseWindow(t *testing.T) {
	r := newRig(t)
	code, _ := r.state.BeginPairing(bg)
	for i := 0; i < 5; i++ {
		if res, _ := r.state.Claim(bg, 9, 9, "00000000a"+strconv.Itoa(i)); res != WrongCode {
			t.Fatalf("attempt %d = %v", i, res)
		}
	}
	if res, _ := r.state.Claim(bg, ownerChat, ownerUser, code.Code); res == Claimed {
		t.Error("the right code worked after five wrong ones")
	}
	// Chatter that cannot be a code never counts.
	r2 := newRig(t)
	good, _ := r2.state.BeginPairing(bg)
	for i := 0; i < 10; i++ {
		r2.state.Claim(bg, 9, 9, "hello there")
	}
	if res, _ := r2.state.Claim(bg, ownerChat, ownerUser, good.Code); res != Claimed {
		t.Error("ordinary chatter closed the window")
	}
}

func TestTelegram_Pairing_RefusedWhilePaired(t *testing.T) {
	r := newRig(t)
	r.pair()
	if _, err := r.state.BeginPairing(bg); err != ErrPaired {
		t.Errorf("err = %v", err)
	}
	if _, _, err := r.mgr.BeginPairing(bg); err == nil || !strings.Contains(err.Error(), "unpair") {
		t.Errorf("manager err = %v", err)
	}
}

func TestTelegram_Unpair_InvalidatesTokensAndCodesKeepsBotToken(t *testing.T) {
	r := newRig(t)
	r.pair()
	tok, err := r.state.IssueToken(bg, ownerChat, "eval", "https://jobs.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.mgr.Unpair(bg); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.state.Owner(bg); ok {
		t.Error("still paired")
	}
	if _, err := r.vault.Get(TokenSecret); err != nil {
		t.Errorf("unpairing removed the bot token: %v", err)
	}
	// Pair again: the old button is refused although the chat is the same.
	code, _ := r.state.BeginPairing(bg)
	r.state.Claim(bg, ownerChat, ownerUser, code.Code)
	if err := r.state.UseToken(bg, tok, ownerChat, ""); err != ErrTokenUnknown {
		t.Errorf("a button from before the re-pairing: %v", err)
	}
}
