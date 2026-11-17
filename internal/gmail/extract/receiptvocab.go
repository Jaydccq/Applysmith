package extract

import (
	"applysmith/internal/lazyregexp"
)

// Phrase is one vocabulary rule: a case-insensitive pattern and the rule id
// the classifier records when it fires.
type Phrase struct {
	Re *lazyregexp.Regexp
	ID string
}

func phrases(pairs ...string) []Phrase {
	out := make([]Phrase, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, Phrase{lazyregexp.New("(?i)" + pairs[i]), pairs[i+1]})
	}
	return out
}

// ReceiptPhrases is the application-receipt vocabulary. The classifier and the
// receipt lookup both read this one list, so they cannot drift: a receipt that
// the classifier recognises is a receipt the lookup can confirm.
var ReceiptPhrases = phrases(

	`\bthank you for applying\b`, "applied.receipt.thank_you_for_applying",
	`\bthanks for applying\b`, "applied.receipt.thanks_for_applying",
	`\bthanks so much for applying\b`, "applied.receipt.thanks_so_much_for_applying",
	`\breceived your application\b`, "applied.receipt.received_your_application",
	`\bwe have received your application\b`, "applied.receipt.we_have_received",
	`\byour application has been received\b`, "applied.receipt.has_been_received",
	`\bsubmitted your application\b`, "applied.receipt.submitted_your_application",
	`\bthank you for taking the first step\b`, "applied.receipt.taking_the_first_step",
	`\bthank you for (?:taking the time to apply|taking time to apply|taking the time to complete your application)\b`, "applied.receipt.taking_the_time_to_apply",
	`\bthank you for submitting your (?:application|resume|profile)\b`, "applied.receipt.submitting_your_application",
	`\bthanks for submitting your (?:application|resume|profile)\b`, "applied.receipt.thanks_submitting",
	`\bthank you (?:again )?for taking the time to (?:apply|complete (?:your )?application)\b`, "applied.receipt.taking_time_complete",
	`\bthank you for your application (?:for|to)\b`, "applied.receipt.your_application_for",
	`\bthanks for considering\b`, "applied.receipt.thanks_for_considering",
	`\bsuccessfully received your application\b`, "applied.receipt.successfully_received",
	`\bwe('?ve| have) received your application\b`, "applied.receipt.we_have_received_alt",
	`\byour application (?:has been received|was received|is received|received)`, "applied.receipt.application_received",
	`\byour application\b[\s\S]{0,80}?\bhas been received\b`, "applied.receipt.your_application_has_been_received",
	`\bthank you for your application\b`, "applied.receipt.thank_you_for_your_application",
	`\b(?:we |we'?ve |we have )?received your (?:job |new |online |latest )?application\b`, "applied.receipt.received_your_x_application",
	`\byour\s+[a-z0-9 .'&-]+\s+application is in\b`, "applied.receipt.your_x_application_is_in",
	`\bapplication is in!?`, "applied.receipt.application_is_in",
	`\bapplication has been (?:received|confirmed)\b`, "applied.receipt.application_has_been_confirmed",
	`\bapplication submitted\b`, "applied.receipt.application_submitted",
	`\byour application has been successfully submitted\b`, "applied.receipt.application_successfully_submitted",
	`\bsuccessful application submission\b`, "applied.receipt.successful_application_submission",
	`\byour application was sent to\b`, "applied.receipt.application_was_sent",
	`\byour application (?:was|has been) viewed by\b`, "applied.status.application_viewed",
	`\bthank you for your interest\b[\s\S]{0,160}?\bwe (?:have )?received\b`, "applied.receipt.interest_plus_received",
)
