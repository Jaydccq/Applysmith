package apply

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/vault"
)

func explicitSignupRun() Input {
	return Input{URL: loginURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true, UserStarted: true}
}

// A generated password is stored only once a create-account form was found; a
// page that never shows one leaves nothing behind that a later run would read as
// "an account already exists".
func TestApply_Signup_NoFormFoundSavesNoPassword(t *testing.T) {
	a := newAcct(t, explicitSignupRun(), ATSGeneric, "careers.example.test", signupMod("United States"))
	a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	a.tab.onClick = func(tab *scriptTab, c Control) {
		if c.Labels[0] == "Sign in" {
			tab.typed = map[int]string{}
			tab.set(loginPage("Invalid email or password"))
		} // "Create account" leads nowhere: the page stays the sign-in wall
	}
	_, st := a.run()
	if st == nil || !strings.Contains(st.reason, "no create-account form appeared") {
		t.Fatalf("stop %+v", st)
	}
	if _, err := a.v.Get("signin-site-careers.example.test"); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("a password was saved though no form was found: %v", err)
	}
	if a.g.count(`SELECT count(*) FROM signup_attempts`) != 0 {
		t.Fatal("an attempt was counted before any form was found")
	}
	if a.g.count(`SELECT count(*) FROM credential_index WHERE name = 'signin-site-careers.example.test'`) != 0 {
		t.Fatal("the index names a password that was never stored")
	}
}

// A password that already existed under the secret's name is the user's: a
// refused submit must not delete it.
func TestApply_Signup_ACallerChosenPasswordSurvivesARefusedSubmit(t *testing.T) {
	a := newAcct(t, explicitSignupRun(), ATSGeneric, "careers.example.test", signupMod("United States"))
	// Stored with no index row and no default email, so it is not a sign-in
	// candidate and the stage reaches the sign-up lane with it in place.
	if _, err := a.v.Set("signin-site-careers.example.test", "Chosen-By-User-1!"); err != nil {
		t.Fatal(err)
	}
	signupPages(a, true)
	_, st := a.run()
	if st == nil || !strings.Contains(st.reason, "no account was created") {
		t.Fatalf("stop %+v", st)
	}
	if got, err := a.v.Get("signin-site-careers.example.test"); err != nil || got != "Chosen-By-User-1!" {
		t.Fatalf("the user's password was removed or replaced: %q %v", got, err)
	}
}

// A sign-up that fails before its form was sent created no account, so the
// password it generated is taken back out of the vault: a later run must not
// read the host as one with an account.
func TestApply_Signup_AFailureBeforeTheFormIsSentLeavesNoPassword(t *testing.T) {
	const name = "signin-site-careers.example.test"
	// redrawn is the sign-up form after the page replaced its confirm box.
	redrawn := func(st *PageState) *PageState {
		c := *st
		c.Inputs = append([]InputInfo(nil), st.Inputs...)
		c.Inputs[3].Type = "text"
		return &c
	}
	for _, c := range []struct {
		name, reason string
		break_       func(tab *scriptTab)
	}{
		{"a password box that is no password box at typing", "is not a password box", func(tab *scriptTab) {
			form := tab.state
			tab.set(redrawn(form))
			tab.queue = []*PageState{form, tab.state}
		}},
		{"a create-account control that is covered", "covered", func(tab *scriptTab) { tab.covered = map[int]bool{2: true} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newAcct(t, explicitSignupRun(), ATSGeneric, "careers.example.test", signupMod("United States"))
			a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
			signupPages(a, false)
			base := a.tab.onClick
			a.tab.onClick = func(tab *scriptTab, ctl Control) {
				if ctl.Labels[0] == "Sign in" {
					tab.typed = map[int]string{}
					tab.set(loginPage("Invalid email or password"))
					return
				}
				opens := tab.state.Title != "Create account"
				base(tab, ctl)
				if opens {
					c.break_(tab)
				}
			}
			_, st := a.run()
			if st == nil || !strings.Contains(st.reason, c.reason) {
				t.Fatalf("stop %+v", st)
			}
			if _, err := a.v.Get(name); !errors.Is(err, vault.ErrNotFound) {
				t.Fatalf("the password of an account that was never created was kept: %v", err)
			}
		})
	}
	t.Run("a combined form whose password box is gone at typing", func(t *testing.T) {
		a := newAcct(t, explicitSignupRun(), ATSGeneric, "careers.example.test", signupMod("United States"))
		a.e.combined = &combinedAccount{host: "careers.example.test", name: name, system: ATSGeneric, tenant: "careers.example.test"}
		page := combinedPage(loginURL)
		a.tab.set(redrawn(page))
		if stp := a.s.combinedType(context.Background(), page); stp == nil || !strings.Contains(stp.reason, "is not a password box") {
			t.Fatalf("stop %+v", stp)
		}
		if _, err := a.v.Get(name); !errors.Is(err, vault.ErrNotFound) {
			t.Fatalf("the password of a combined form that was never sent was kept: %v", err)
		}
	})
}

// "Verify your email" proves the password, so the credential that reached it is
// kept as the tenant's key before the activation mail is even looked for.
func TestApply_Account_VerifyYourEmailKeepsTheProvenSharedDefaultAsTheTenantKey(t *testing.T) {
	v := newVerifyRig(t, 1, func(gmail.LinkQuery, int) (gmail.LinkResult, error) {
		return wdLink("Ab12Cd34Ef56", t0.Add(-48*time.Hour)), nil
	})
	if err := v.a.cs.Remove(context.Background(), "tenant-workday-example"); err != nil { // start with no tenant key
		t.Fatal(err)
	}
	v.a.seed(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	if d, st := v.a.run(); st != nil || !strings.Contains(d, "activated and signed in") {
		t.Fatalf("detail %q stop %+v", d, st)
	}
	if got := vaultValue(t, v.a.v, "tenant-workday-example"); got != "Default-Pw-1!x" {
		t.Fatalf("the proven password was not kept as the tenant key: %q", got)
	}
	if got := vaultValue(t, v.a.v, "signin-default"); got != "Default-Pw-1!x" {
		t.Fatal("the shared default changed")
	}
}
