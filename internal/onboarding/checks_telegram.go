package onboarding

import (
	"context"

	"applysmith/internal/notify/telegram"
	"applysmith/internal/vault"
)

// Telegram is optional: its checks skip when the user skipped the step and warn,
// never fail, when it was never set up.
func telegramChecks() []Check {
	return []Check{
		{ID: "telegram.token", Run: checkTelegramToken},
		{ID: "telegram.pairing", Run: checkTelegramPairing},
	}
}

func checkTelegramToken(ctx context.Context, env *Env) Outcome {
	if env.Vault == nil {
		return Outcome{Status: Warn, Summary: "the keychain is not available, so Telegram cannot be checked"}
	}
	if _, err := env.Vault.Get(telegram.TokenSecret); err != nil {
		if vault.IsCode(err, vault.CodeNotFound) {
			return Outcome{Status: Warn, Summary: "no Telegram bot token is stored; notifications go to the desktop only",
				Fix: "create a bot with BotFather and run `applysmith telegram set-token --stdin`"}
		}
		return Outcome{Status: Warn, Summary: "the Telegram bot token could not be read"}
	}
	return Outcome{Status: Pass, Summary: "a bot token is stored"}
}

func checkTelegramPairing(ctx context.Context, env *Env) Outcome {
	if env.Store == nil {
		return Outcome{Status: Skipped, Summary: "no workspace database"}
	}
	if _, ok := telegram.NewState(env.Store, env.Now).Owner(ctx); ok {
		return Outcome{Status: Pass, Summary: "a chat is paired"}
	}
	return Outcome{Status: Warn, Summary: "no chat is paired", Fix: "run `applysmith telegram pair` and send the code to the bot"}
}
