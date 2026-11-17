# Captcha solver

`internal/captcha` asks the user's own 2Captcha account for a token when the safety gate (`gates.CaptchaSolve`, see `gates.md`) allows it. Bounds and refusals are the `apply-safety-gates` requirement "Captcha solving through the user's own 2Captcha account"; the constants live in `internal/gates/captcha.go` and are read, not copied.

- One `Session` per run, built with the run kind, the `captcha_solver` policy value, a key reader (the `twocaptcha-key` secret) and a skip-list reader. The package imports no vault or workspace code; the caller supplies those facts.
- The gate is asked before every request. The balance read (once per run) is preceded by a gate call that checks everything except the balance, so a refusal for run kind, key, switch, kind, skip list or cap makes no request at all.
- API: `createTask` / `getTaskResult` / `getBalance` on `api.2captcha.com`, proxyless task types. Polling follows `gates.CaptchaPollStep`; task creation is retried once after a 5xx.
- Settings, Captcha solver, Test key reads the balance once on the user's press (`captcha.Balance`, `POST /api/secrets/{name}/test`): the key works with a balance, the provider does not know it (`ERROR_KEY_DOES_NOT_EXIST`), or the read failed; a balance under the floor says the solver will leave challenges to the user.
- `Solver` is the interface the chain's page steps call: `Solve`, `TokenRefused` (one more solve, inside the cap, then the stop is the user's) and `Detail` (the `captcha: ...` lines for the stage detail). Putting the token into the page is the chain's job.
- A `Solution` prints a placeholder under every formatting, logging and encoding path; only `Token()` returns the value. Errors are built from the service's error code and the HTTP status only.
- A malformed reply (missing `errorId`, no task id, an unknown status, an empty or spaced token, a bad balance) fails the solve; an unreadable balance refuses it.
- The repository rule `captcha-scope` (`internal/guard/captcha.go`) names the paths allowed to mention the provider or its setting and the packages allowed to import the solver. A new call site is added to those lists in the same change.
