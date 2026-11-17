// Package assist runs the three model-assisted lanes of the answer bank after
// the deterministic matcher: entry recognition, the grounded option fallback
// and the free-text composer. It owns what makes a model answer safe to type
// into a form: which questions may reach a model at all, the budgets and the
// reply cache, the evidence a choice must cite, the grounding checks on
// composed prose, the sanitising of posting and company text, and the company
// context taken only from the website a posting declares.
//
// Every model call goes through the routed model of internal/llm, so each is
// recorded in the call ledger. The matcher stays pure; this package is where
// the clock, the cache and the network meet it.
package assist
