package memory

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Screening is what keeps stored text from steering the agent. Three
// different things are caught: text aimed at the assistant, which is dropped;
// a statement that tells the agent how to answer, which waits for the user;
// and, for rules, wording that would widen what the agent may do.

// hardInjection is wording no fact and no rule may carry.
var hardInjection = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\b(ignore|disregard|forget|override)\b[^.\n]{0,40}\b(previous|prior|above|earlier|all|any|your|the)\b[^.\n]{0,30}\b(instructions?|rules?|prompts?|guidelines?|constraints?)\b`),
	lazyregexp.New(`(?i)(^|\n)\s*(system|assistant|developer|user|human|ai)\s*[:>]`),
	lazyregexp.New(`(?i)<\|?\s*(im_start|im_end|system|assistant|endoftext)\b`),
	lazyregexp.New(`(?i)\b(you are now|you must now|from now on,? you|act as (if|though)? ?(you|an?)|pretend (to be|you))\b`),
	lazyregexp.New(`(?i)\b(reveal|print|show|repeat)\b[^.\n]{0,30}\b(system prompt|your instructions|hidden instructions)\b`),
}

// bareImperative is addressed to the assistant. A rule is exactly that, so
// only a fact is screened with it.
var bareImperative = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)^\s*(you (must|should|shall|will|need to)|always|never|do not|don't)\b[^.\n]{0,60}\b(approve|confirm|submit|answer|reply|respond|call|run|execute|send|tell)\b`),
	lazyregexp.New(`(?i)\b(approve|accept|confirm) (everything|all|any)\b`),
}

// adv lets any run of adverbs ("explicitly", "hereby", "already") stand between
// the words of a pattern; a pattern anchored to the next word misses them.
const adv = `(?:(?:hereby|already|\w+ly) )*`

var instructionPatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\b(i|we) (always|never) ` + adv + `(answer|say|reply|respond|choose|select|pick|tick|check|click|put)\b`),
	lazyregexp.New(`(?i)\balways ` + adv + `(answer|say|reply|respond|choose|select|pick|tick|check)\b[^.\n]{0,40}\b(yes|no|true|false|agree|decline)\b`),
	lazyregexp.New(`(?i)\b(the user|i|we) (?:(?:has|have|had|am|are) )?` + adv + `(authori[sz]ed|authori[sz]es|allowed|approved|permitted|consented)\b`),
	lazyregexp.New(`(?i)\b(i|we) ` + adv + `(authori[sz]e|allow|permit|consent to|give you permission)\b`),
	lazyregexp.New(`(?i)\b(without|no need to) (ask|asking|confirm|confirming|confirmation|checking with me)\b`),
	lazyregexp.New(`(?i)\b(treat|consider|assume) (it|this|that|everything)\b[^.\n]{0,30}\b(approved|authori[sz]ed|confirmed|allowed)\b`),
	lazyregexp.New(`(?i)\bwhen(ever)? (a |an |the )?(form|application|question|site|page)\b[^.\n]{0,60}\b(ask|asks|asking)\b[^.\n]{0,40}\b(answer|say|put|enter|select)\b`),
}

var permissionPatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\b(approve|accept|confirm) (everything|all|any)\b`),
	lazyregexp.New(`(?i)\b(submit|apply|register|sign up|send|delete|forget|withdraw)\b[^.\n]{0,40}\b(without|no need)\b[^.\n]{0,20}\b(ask|asking|confirm|confirming|confirmation|approval|permission|review|checking)\b`),
	lazyregexp.New(`(?i)\b(always|automatically|auto)[ -](submit|approve|apply|accept|confirm|authori[sz]e|register)\b`),
	lazyregexp.New(`(?i)\b(never|don't|do not|stop|no need to) (ask|asking|confirm|confirming|check|checking|prompt|prompting)\b`),
	lazyregexp.New(`(?i)\b(skip|bypass|disable|turn off|switch off|relax|waive|override|ignore|remove)\b[^.\n]{0,30}\b(confirmations?|approvals?|gates?|guards?|checks?|permissions?|safeguards?|restrictions?|limits?)\b`),
	lazyregexp.New(`(?i)\b(grant|give|allow|enable|authori[sz]e)\b[^.\n]{0,30}\b(permissions?|access|submitting|submission|autonomy)\b`),
	lazyregexp.New(`(?i)\b(is|are|you are|you're|you have) ` + adv + `(allowed|permitted|authori[sz]ed|free) to\b`),
	lazyregexp.New(`(?i)\bthe user (?:has )?` + adv + `(authori[sz]ed|approved|allowed|permitted)\b`),
	lazyregexp.New(`(?i)\btreat\b[^.\n]{0,40}\b(as )?(approved|authori[sz]ed|confirmed)\b`),
}

func matchesAny(ps []*lazyregexp.Regexp, s string) bool {
	for _, p := range ps {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// InjectionShaped reports whether text is aimed at the assistant itself:
// instructions to drop its rules, role-framed lines, control tokens and bare
// imperatives about approving or answering.
func InjectionShaped(s string) bool {
	return matchesAny(hardInjection, s) || matchesAny(bareImperative, s)
}

// ScreenRule reports why a line of instruction text may not stand as a rule: it
// is empty or longer than a rule may be, is aimed at the assistant's own
// instructions, or would change what the agent may do. A skill's instruction
// block is screened line by line with it.
func ScreenRule(line string) error {
	_, err := checkRule(line)
	return err
}

// ruleInjection is the screening of a rule, which may address the assistant.
func ruleInjection(s string) bool { return matchesAny(hardInjection, s) }

// InstructionShaped reports whether text tells the agent how to answer or what
// the user has authorised, which a fact must never do.
func InstructionShaped(s string) bool { return matchesAny(instructionPatterns, s) }

// GrantsPermission reports whether a rule would change what the agent may do.
func GrantsPermission(s string) bool { return matchesAny(permissionPatterns, s) }

var (
	emailRE  = lazyregexp.New(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	digitsRE = lazyregexp.New(`\d[\d\s().-]{7,}\d`)
)

// piiLevel is high when the text carries an address, a phone-like number or a
// long digit run, and low otherwise: a fact about the user is never PII-free.
func piiLevel(s string) string {
	if emailRE.MatchString(s) || digitsRE.MatchString(s) {
		return "high"
	}
	return "low"
}

// normalize folds text to the form the tombstone and the duplicate check
// compare: lower case, single spaces.
func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
