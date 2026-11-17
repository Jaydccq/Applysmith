package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/notify/telegram"
	"applysmith/internal/remote/qr"
	"applysmith/internal/vault"
)

const telegramUsage = `usage:
  applysmith telegram set-token --stdin
  applysmith telegram clear-token
  applysmith telegram pair
  applysmith telegram unpair
  applysmith telegram status [--json]

set-token reads a bot token from standard input (create one with BotFather), checks it with Telegram and stores it in the keychain
pair prints a one-time code to send to the bot from a private chat; it is valid for 10 minutes
every command takes --workspace <dir>
`

func (e *env) telegramCmd(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, telegramUsage)
		return 2
	}
	fs, parse := e.subFlags("telegram", args[0], telegramUsage)
	ws := fs.String("workspace", "", "workspace directory")
	stdin := fs.Bool("stdin", false, "read the token from standard input")
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, ok := parse(args[1:]); !ok || len(pos) > 0 {
		return 2
	}
	name := "telegram " + args[0]
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail(name, err)
	}
	wsp, st, release, err := e.openStore(ctx, root, accessShared)
	if err != nil {
		return e.fail(name, err)
	}
	defer release()
	id, err := wsp.ID()
	if err != nil {
		return e.fail(name, err)
	}
	v, err := vault.New(id, e.backend, vault.WithGetenv(e.getenv))
	if err != nil {
		return e.fail(name, err)
	}
	state := telegram.NewState(st, nil)
	mgr := &telegram.Manager{State: state, Vault: v, API: e.telegramAPI}
	switch args[0] {
	case "set-token":
		if !*stdin {
			fmt.Fprint(e.stderr, telegramUsage)
			return 2
		}
		tok, err := e.readSecret("Telegram bot token")
		if err != nil {
			return e.fail(name, errors.New("the token could not be read"))
		}
		user, err := mgr.SetToken(ctx, tok)
		switch {
		case errors.Is(err, telegram.ErrTokenShape):
			e.fail(name, err)
			return 2
		case err != nil:
			return e.fail(name, err)
		}
		fmt.Fprintf(e.stdout, "stored the token for @%s\n", user)
		return 0
	case "clear-token":
		if err := mgr.ClearToken(ctx); err != nil {
			return e.fail(name, err)
		}
		fmt.Fprintln(e.stdout, "the bot token was removed; polling stops")
		return 0
	case "pair":
		code, bot, err := mgr.BeginPairing(ctx)
		if err != nil {
			return e.fail(name, err)
		}
		send := "Send"
		if link := telegram.StartLink(bot, code.Code); link != "" {
			fmt.Fprintf(e.stdout, "Open %s and press Start, or scan this with your phone:\n", link)
			if modules, err := qr.Encode(link); err == nil {
				fmt.Fprint(e.stdout, qr.Text(modules))
			}
			send = "Or send"
		}
		fmt.Fprintf(e.stdout, "%s this code to @%s from a private chat within %d minutes:\n%s\n", send, bot, int(telegram.PairingTTL/time.Minute), code.Code)
		return 0
	case "unpair":
		if err := mgr.Unpair(ctx); err != nil {
			return e.fail(name, err)
		}
		fmt.Fprintln(e.stdout, "unpaired; every pending code and button is invalid, the bot token is kept")
		return 0
	case "status":
		_, tokErr := v.Get(telegram.TokenSecret)
		_, paired := state.Owner(ctx)
		health := "stopped"
		if tokErr == nil {
			health = "configured"
			if state.LeaseHeld(ctx) {
				health = "healthy"
			}
		}
		out := wire.TelegramStatus{TokenSet: tokErr == nil, BotUsername: state.Bot(ctx), Paired: paired, Health: health, Unauthorized: wire.SafeInt(state.Unauthorized(ctx))}
		if *asJSON {
			return e.printJSON(out)
		}
		fmt.Fprintf(e.stdout, "token: %v\nbot: %s\npaired: %v\nhealth: %s\nunauthorized attempts: %d\n", out.TokenSet, out.BotUsername, paired, health, out.Unauthorized)
		return 0
	}
	fmt.Fprint(e.stderr, telegramUsage)
	return 2
}
