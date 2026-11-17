package match

// Predicates the model-assisted lanes call. They are the same functions the
// resolvers use, exported so the lanes never keep a second list of what is
// sensitive, unsafe or a trap.

// IsSensitiveCategoryQuestion reports a text naming a protected characteristic.
func IsSensitiveCategoryQuestion(text string) bool { return isSensitiveCategoryQuestion(text) }

// IsDisqualifyingFactDisclosure reports a text asking about or stating a fact
// that could disqualify the applicant.
func IsDisqualifyingFactDisclosure(text string) bool { return isDisqualifyingFactDisclosure(text) }

// IsAttestationDefaultUnsafe is the gate an affirmative default must pass.
func IsAttestationDefaultUnsafe(text string) bool { return isAttestationDefaultUnsafe(text) }

// IsGenerativelyUnsafeQuestion reports a slot no generative lane may fill.
func IsGenerativelyUnsafeQuestion(text string) bool { return isGenerativelyUnsafeQuestion(text) }

// IsLeaveBlankInstruction reports a form that tells a person to leave the
// control empty.
func IsLeaveBlankInstruction(text string) bool { return isLeaveBlankInstruction(text) }

// IsWithdrawalLabel reports an option label that ends the candidacy.
func IsWithdrawalLabel(label string) bool { return isWithdrawalLabel(label) }

// IsPlaceholderOption reports an option label that is the control's own prompt.
func IsPlaceholderOption(label string) bool { return isPlaceholderOption(label) }

// IsNarrativeText reports a label that asks the applicant for prose.
func IsNarrativeText(text string) bool { return isNarrativeText(text) }

// IsEssayQuestion reports an essay ask.
func IsEssayQuestion(text string) bool { return isEssayQuestion(text) }

// AsksHeldCompensation reports a question about pay the applicant already holds.
func AsksHeldCompensation(text string) bool { return asksHeldCompensation(text) }

// IsParentReferential reports a follow-up slot pointing back at the previous
// question.
func IsParentReferential(text string) bool { return isParentReferential(text) }

// TrapAbstain reports a hidden control, a honeypot or a question that addresses
// an AI with a directive, and the trace line explaining it.
func TrapAbstain(q Question) (string, bool) { return trapAbstain(q) }

// IsSelfIdentificationSet reports an option list that forms a
// self-identification set.
func IsSelfIdentificationSet(opts []Option) bool { return readSelfIDSet(opts) != nil }

// IsTargetCompensationEntry reports an entry that states a target
// compensation, which is never a fact about pay the applicant holds.
func IsTargetCompensationEntry(e Entry) bool {
	return e.Kind == KindCompensationRange || (e.Kind == KindFixed && compTargetAnswerRE.MatchString(e.Answer))
}

// IsProficiencyLadder reports options the proficiency-ladder lane reads as a
// scale, so a caller fetches knowledge evidence only for questions that use it.
func IsProficiencyLadder(opts []Option) bool {
	_, ok := readProficiencyLadder(realOptions(opts))
	return ok
}

// LeastLadderOption is the one lowest rung of options the proficiency-ladder
// lane reads as a scale ("Beginner" of Expert, Advanced, Beginner).
func LeastLadderOption(opts []Option) (Option, bool) {
	return leastLadderOption(realOptions(opts))
}

// IsWorkAuthOrPayEntry reports an entry that states work authorization,
// sponsorship need or pay, by category, kind or the work-fact entry ids. The
// option lane never offers such an entry as evidence.
func IsWorkAuthOrPayEntry(e Entry) bool {
	return IsWorkAuthEntry(e) || e.Category == CategoryCompensation || e.Kind == KindCompensationRange
}

// IsWorkAuthEntry reports an entry that states work authorization or
// sponsorship need, by category, the work-fact entry ids or its answer key.
func IsWorkAuthEntry(e Entry) bool {
	return e.Category == CategoryWorkAuthorization || workFactEntries[e.ID] || e.EffectiveAnswerKey() == "work_authorized"
}

// PolarityConflict reports a fact and an option label that share a term the one
// negates and the other affirms ("Will not relocate" against "Relocate to
// Berlin"). It is the polarity test of option binding, so the fallback never
// holds a looser notion of support than the matcher does.
func PolarityConflict(fact, label string) bool {
	return polarityConflictOf(newAnswerProfile(fact), newLabelProfile(Option{Label: label}, "")) != ""
}
