package classify

import (
	"applysmith/internal/gmail/extract"
	"applysmith/internal/lazyregexp"
)

func ci(p string) *lazyregexp.Regexp { return lazyregexp.New("(?i)" + p) }

type rule struct {
	re *lazyregexp.Regexp
	id string
}

func rules(pairs ...string) []rule {
	out := make([]rule, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, rule{ci(pairs[i]), pairs[i+1]})
	}
	return out
}

func firstRule(rs []rule, text string) *rule {
	for i := range rs {
		if rs[i].re.MatchString(text) {
			return &rs[i]
		}
	}
	return nil
}

var hardRejection = rules(
	`\bnot moving forward\b`, "rejection.hard.not_moving_forward",
	`\bwill not be moving forward\b`, "rejection.hard.will_not_be_moving_forward",
	`\bnot (?:being |getting )?selected (?:for|to|at)\b`, "rejection.hard.not_selected_for",
	`\bnot been selected\b`, "rejection.hard.not_been_selected",
	`\bdecided not to proceed\b`, "rejection.hard.decided_not_to_proceed",
	`\bapplication (?:has been|was|is) unsuccessful\b`, "rejection.hard.application_unsuccessful",
	`\bdecided to not move forward with your (?:application|candidacy)\b`, "rejection.hard.decided_to_not_move_forward",
	`\bpursuing (?:another|other|a different)\s+(?:applicants?|candidates?)\b`, "rejection.hard.pursuing_other",
	`\b(?:filled the position|position (?:has|is)(?: now| since)? been filled|position has been put on hold|closed the role|role has been (?:closed|filled|put on hold))\b`, "rejection.hard.position_filled",
	`\bpause(?:d|s|ing)? hiring for (?:this|the) role\b`, "rejection.hard.pause_hiring_for_role",
	`\b(?:unable to|can(?:not|\s+not))\s+(?:proceed|move forward)\s+(?:with\s+(?:your\s+)?(?:application|candidacy|interview|process)|further\s+with\s+(?:your\s+)?(?:application|candidacy)|for\s+(?:this|the)\s+(?:role|position))\b`, "rejection.hard.unable_to_move_forward",
	`\bnot able to offer\b`, "rejection.hard.not_able_to_offer",
	`\bregret to inform\b`, "rejection.hard.regret_to_inform",
	`\bmoving in a different direction\b`, "rejection.hard.different_direction",
	`\b(?:will not(?: be)?|we are not|are not)\s+advancing\b`, "rejection.hard.not_advancing",
	`\b(?:we |we'?ve |we have )?won't be advancing\b`, "rejection.hard.wont_be_advancing",
	`\b(?:does|do|did)\s+not\s+meet\s+(?:one of |the )?(?:our|the|minimum|required)\s*(?:minimum\s+)?(?:requirements|criteria|qualifications)\b`, "rejection.hard.does_not_meet_requirements",
	`\bwill not be (?:taking|moving) (?:your )?application forward\b`, "rejection.hard.application_not_forward",
	`\b(?:moved|moving) forward with (?:another|a different|other|a)\s+candidate`, "rejection.hard.moved_forward_with_other",
	`\bdecided to (?:move forward with|pursue) (?:another|other|a different)\s+(?:candidate|applicant)`, "rejection.hard.decided_other_candidate",
	`\bdecided not to move forward with your (?:application|candidacy)\b`, "rejection.hard.decided_not_move_forward",
	`\b(?:we|we'?ve|we have) decided not to move forward\b`, "rejection.hard.we_decided_not_move_forward",
	`\b(?:decided|chose|chosen|opted) to (?:move (?:forward|ahead)|proceed|progress|continue) with (?:another|other|a different|a)?\s*candidate`, "rejection.hard.decided_to_move_with_candidate",
	`\bdecided to (?:move (?:forward|ahead)|proceed|progress) with candidates whose\b`, "rejection.hard.decided_with_candidates_whose",
	`\bhave (?:closed|filled)(?:\s+or\s+(?:closed|filled))?\s+(?:this\s+|the\s+)?(?:position|role|job|opening|req)\b`, "rejection.hard.closed_or_filled",
	`\b(?:position|role|job|opening|req)\s+(?:has|is)(?:\s+been)?\s+(?:closed|filled)\b`, "rejection.hard.position_closed_filled",
	`\b(?:extended|made|presented) an offer to (?:another|a different|a)\s*candidate`, "rejection.hard.offer_extended_to_other",
	`\b(?:choice|decision|decided|chose|chosen|opted) not to (?:move forward|proceed) with your (?:application|candidacy)\b`, "rejection.hard.choice_not_to_move_forward",
	`\bnot be (?:progressing|proceeding) with your (?:application|candidacy)\b`, "rejection.hard.not_progressing_with_application",
)

var softRejection = rules(
	`\bunfortunately\b`, "rejection.soft.unfortunately",
	`\bnot (?:an? )?(?:ideal|right) fit\b`, "rejection.soft.not_ideal_fit",
	`\bother (?:candidates?|applicants?)\b`, "rejection.soft.other_candidates",
	`\bmore aligned with\b`, "rejection.soft.more_aligned_with",
	`\bpursu(?:e|ing) (?:another|other|a different)\s+(?:applicants?|candidates?)\b`, "rejection.soft.pursue_other",
)

// receiptRules adapts the shared receipt vocabulary.
var receiptRules = func() []rule {
	out := make([]rule, len(extract.ReceiptPhrases))
	for i, p := range extract.ReceiptPhrases {
		out[i] = rule{p.Re, p.ID}
	}
	return out
}()

var (
	hiringNounRE    = ci(`\b(?:application|candidacy|interview|role|position|opportunity|opening|requisition)\b`)
	hiringContextRE = ci(`\b(?:your (?:application|candidacy|candidate profile)|application (?:for|to|with|at|process)|thank(?:s| you) for applying|applying to work at|interest in joining|applicant|candidate(?: profile| assessment| test| screening)?|recruit(?:er|ers|ing|ment)|hiring (?:process|assessment|assessments|team)|talent acquisition|(?:the|this|your|our|a|an) (?:role|position|job|opening|requisition)|(?:role|position|job|opening|requisition) (?:at|with|for))\b`)
	jobVocabularyRE = ci(`\b(?:interview(?:s|ed|ing|ers?)?|candidate|recruit(?:er|ers|ing|ment)|careers?|hiring|position|role|job|opening|applicant)\b`)

	offerRE          = ci(`\b(offer letter|(?:extend(?:ing)?|send(?:ing)?|make|made|present(?:ing|ed)?) (?:you )?(?:an )?(?:employment )?offer|(?:job|employment) offer(?: from| for you| to you)?|offer for (?:you|your candidacy|your application)|compensation package|start date|accept (?:the )?offer)\b`)
	offerIntentRE    = ci(`\b(offer letter|employment offer|(?:extend(?:ing)?|send(?:ing)?|make|made|present(?:ing|ed)?) (?:you )?(?:an )?(?:employment )?offer|compensation package|start date|accept (?:the )?offer|offer deadline)\b`)
	explicitOfferRE  = ci(`\b(offer letter|(?:pleased|delighted|happy|excited|thrilled) to (?:extend|offer|send|share)|(?:extend(?:ing|ed)?|send(?:ing)?|make|made|present(?:ing|ed)?) you (?:an? )?(?:formal |verbal |written )?(?:employment |job )?offer|accept (?:the |this |our )?offer)\b`)
	candidateSpecRE  = ci(`\b(your (?:application|candidacy|candidate profile|offer|offer letter|compensation|start date)|you(?:'re| are| have been| will be)?|pleased to offer|we are delighted to offer|we would like to offer|congratulations)\b`)
	newsletterRE     = ci(`\b(newsletter|job alert|jobs? alert|morning coffee|sunday '?spresso|career tips|career paths?|early careers|latest jobs|recommended jobs|picked for you|similar jobs|unsubscribe|view all jobs|job offer tips|landing a job offer)\b`)
	pureNewsletterRE = ci(`\b(?:newsletter|weekly digest|read (?:the )?full post|unsubscribe)\b`)
	newsletterCtxRE  = ci(`\b(?:candidates?|recruiters?|recruiting|careers?|hiring|positions?|roles?|jobs?|openings?|applicants?)\b`)
	jobAlertLowRE    = ci(`\b(job alert|jobs? alert|latest jobs below|join our talent network|your future career starts here|gallup exceptional workplace|recommended jobs|picked for you|view all jobs|unsubscribe)\b`)
	pureReceiptSubj  = ci(`^\s*(?:thank you for (?:your )?application!?|successful application submission|application (?:received|submitted|confirmation))\s*$`)
	practiceToolRE   = ci(`\b(ai interview buddy|practice interview|instant answers to any question|real-time transcription)\b`)
	identityVerifyRE = ci(`\b(confirm your identity|please confirm your identity|one[- ]time passcode|verification code|security code|code will expire|use this code to|enter this code)\b`)
	screeningActRE   = ci(`\b(screening questions?|identity verification|verify your identity|complete (?:the |your )?verification)\b`)
	hackathonRE      = ci(`\b(?:hackathon|build week|submission to .+ started)\b`)
	offerDisclaimer  = ci(`\b(?:not|isn'?t|is not|does not constitute)\s+(?:an?\s+)?(?:employment\s+|job\s+)?offer\b`)
	surveyRE         = ci(`\b(candidate experience survey|we value your feedback|value your feedback|share your feedback|feedback survey|survey regarding (?:your )?(?:candidate|application|interview)|feedback at key stages)\b`)

	interviewRE     = ci(`\b(interview invitation|your interview|interview schedul(?:e|ing|ed)|interview is scheduled|meeting is scheduled|self-schedule|schedule (?:an? |your |the )?(?:(?:video|technical|initial|introductory|discussion) )?(?:interview|call|phone screen|meeting|chat|discussion call)|google meet|microsoft teams meeting|teams\.microsoft\.com|zoom(?:\s+video)?\s+(?:call|meeting)|first call|exploratory call|intro call|phone screen|onsite|technical screen|invite you (?:to|for) (?:a |an )?(?:initial |intro |exploratory |first )?(?:(?:zoom|google meet)\s+)?(?:video\s+)?(?:call|chat|interview|meeting)|invite you (?:to|for) (?:a |an )?\d+[- ]minute (?:call|chat|interview|meeting)|would you be able to meet|does that work for you|can you send me an invite|i am free today|are you (?:free|available) (?:to|for) (?:connect|chat|talk|meet)|find a time (?:here|on (?:my |his |her )?calendar|that works)|book (?:a |your )?time|grab a slot|calendly\.com|cal\.com|schedule\.lever\.co|technical interview|interview with our (?:team|founder|ceo|engineering))\b`)
	reminderRE      = ci(`\b(reminder[:\s-]+(?:your )?(?:interview|call|meeting)|(?:your )?(?:interview|call|meeting) (?:is )?(?:today|tomorrow|coming up|starting soon)|scheduled (?:for|on) .{0,80}\b(?:interview|call|meeting)|zcal\.co)\b`)
	reminderSubjRE  = ci(`\breminder\b[\s:\x{2014}-]{0,10}[\s\S]{0,60}?\binterview\b`)
	calendarSubjRE  = ci(`^invitation:[\s\S]{0,120}?\binterview\b`)
	replyNeededRE   = ci(`\b(i was wondering if you would|would like to (?:speak|schedule|connect|chat|talk)|would you be able to meet|does that work for you|can you send me an invite|are you (?:free|available)|i am free today|when (?:are|would) you (?:available|free)|find a time (?:here|on (?:my |his |her )?calendar|that works)|book (?:a |your )?time|grab a slot|could you (?:send|share|confirm|provide)|please (?:reply|respond|confirm|book).{0,80}(?:availability|time|interest|invite))\b`)
	replyOverrideRE = ci(`\b(i was wondering if you would|would like to (?:speak|schedule|connect|chat|talk)|are you (?:free|available) (?:to|for) (?:connect|chat|talk|meet)|available to meet|when (?:are|would) you (?:available|free))\b`)

	coldSourcingRE = ci(`\b(i (?:have )?came? across your profile|came across your (?:profile|resume)|found your (?:resume|profile) on (?:job ?boards|one of the job boards|linkedin)|our client is in need of|explore your interest and availability|if you(?: a|')re interested in this (?:position|role|opportunity|requirement))\b`)

	humanScheduledRE      = ci(`\b(?:(?:i|we)(?:'ve| have)? (?:just |now )?(?:scheduled|booked|set up) (?:the |your |a |an )?(?:[a-z0-9-]+ ){0,4}?(?:interview|call|meeting)\b|sent (?:you )?(?:a |an |the )?(?:google )?(?:calendar )?(?:invite|invitation)\b|calendar invit(?:e|ation)\b|(?:will|to|can) (?:be )?interview(?:ing)? you\b|your interviewer\b|the interview will (?:be|take|last|focus|cover)\b)`)
	namedInterview        = ci(`\b(?:initial|introductory|first|second|final|screening|remote|virtual|technical|onsite|on-site|panel)(?:[- ]round)? (?:\d+[- ]?min(?:ute)?s? )?(?:remote |virtual |video |phone )?interview\b`)
	humanReplyRE          = ci(`\b(?:(?:share|send|let me know|provide|suggest|propose|give me)\b[\w ,'-]{0,40}?\b(?:time[- ]?slots?|availability|available times?|times? that works?)|(?:let me know|tell me) (?:which|what|if)[\w ]{0,24}? works\b|(?:jump|hop|get) on (?:a |an )?(?:quick |brief |short |initial |intro(?:ductory)? |first )?(?:\d+[- ]?min(?:ute)?s? )?(?:remote |video |phone |zoom )?(?:call|chat|meeting)\b|could you (?:please )?(?:send|share|confirm|provide|let me know)\b)`)
	quotedReplyRE         = ci(`\bOn\s+(?:mon|tue|wed|thu|fri|sat|sun)[a-z]*,?\s+[\s\S]{0,120}?\bwrote:|-{2,}\s*Original Message\s*-{2,}|\bFrom:\s[\s\S]{0,200}?\bSent:\s`)
	candidateInterviewCtx = ci(`\b(?:(?:will|to|can) (?:be )?interview(?:ing)? you\b|your interviewer\b|the interview will (?:be|take|last|focus|cover)\b|(?:scheduled|booked) (?:the |your |a |an )?(?:[a-z0-9-]+ ){0,4}?interview\b)`)

	assessmentRE     = ci(`\b(online assessment|general assessment|screening assessment|skills? assessment|pre[- ]employment assessment|assessments? invitation|hiring assessments?|online questionnaire|coding (?:challenge|test|assessment|screener)|technical (?:assessment|screen(?:er|ing))|candidate (?:test|assessment|screen(?:er|ing))|video interview|video screening(?: interview)?|video questions|hackerrank|codesignal|codility|oa\b|(?:take|complete|finish|submit) (?:your |this |the |our )?(?:general |technical |skills? |candidate )?(?:test|assessment|questionnaire|screener|screening|challenge)|(?:take|complete|finish) (?:our |the |your )?(?:\d+[- ]minute )?[a-z0-9 &-]{0,80} screener|complete the (?:hackerrank|coding|technical) (?:test|assessment|challenge|screener)?|assessment for (?:the )?(?:position|role|job)|first round of (?:our |the )?(?:technical )?assessment|candidate/tests/[a-z0-9-]+|invites? you (?:to )?take (?:an?|the) (?:online |technical |skills? |candidate )?(?:assessment|test|challenge))\b`)
	assessmentDoneRE = ci(`\b(?:assessment (?:has been|was|is)?\s*(?:successfully )?(?:submitted|completed|received)|(?:successfully )?(?:submitted|completed|received) (?:your |the )?(?:online |coding |technical )?assessment|(?:thank you|thanks) for (?:taking the time to )?complet(?:e|ing) (?:our |the |your )?(?:online |coding |technical )?assessment|(?:thank you|thanks) for (?:taking the time to )?complet(?:e|ing) .{0,80}\bcoding challenge|we received your .{0,80}assessment submission)\b`)
	takeHomeRE       = ci(`\bnext step[s]?\b[\s\S]{0,80}?\btake[- ]home(?:\s+(?:assignment|assessment|test|challenge|project))?\b`)

	actionRequiredRE = ci(`\b(action required|please complete|deadline|due by|requires your attention)\b`)
	respondedActive  = ci(`\b(would like to (?:speak|schedule|connect|chat)|next step in the process|interview availability|schedule a call|phone screen|when (?:are|would) you (?:available|free))\b`)

	interviewStrongRE = ci(`\b(self-schedule|calendly\.com|cal\.com|schedule\.lever\.co|teams\.microsoft\.com|microsoft teams meeting|schedule (?:an? |your |the )?(?:(?:video|technical|initial|introductory) )?interview|invite you (?:to|for) (?:a |an )?(?:\d+[- ]minute )?(?:initial |intro |exploratory |first )?(?:(?:zoom|google meet)\s+)?(?:video\s+)?(?:call|chat|interview|meeting)|interview with (?:our |the )?(?:founder|ceo|engineering|technical) team)\b`)
	interviewWeakRE   = ci(`\b(schedule (?:a |the |your )?(?:call|interview|meeting|chat|discussion call)|find a time (?:here|on (?:my |his |her )?calendar|that works)|book (?:a |your )?time|grab a slot|are you (?:free|available) (?:to|for) (?:connect|chat|talk|meet))\b`)
	assessmentActRE   = ci(`\b((?:please )?(?:take|complete|finish|submit) (?:this|the|your|our) (?:general |technical |skills? |candidate )?(?:test|assessment|questionnaire|coding test|coding challenge|hackerrank|screener|screening|challenge)|(?:please )?(?:take|complete|finish) (?:our |the |your )?(?:\d+[- ]minute )?[a-z0-9 &-]{0,80} screener|please complete the (?:hackerrank|coding|technical|video|skills?) (?:test|assessment|interview|challenge|screener)?|schedule your (?:assessment|test)|to (?:get|use) (?:the )?(?:link|test link|candidate test link) to (?:get )?started|first round of (?:our |the )?(?:technical )?assessment|candidate/tests/[a-z0-9-]+)\b`)

	conditionalSchedRE = ci(`\b(?:if|when|should)\b[\s\S]{0,140}\b(?:selected|shortlisted|move forward|advance|progress|chosen)\b[\s\S]{0,180}\b(?:schedule|scheduling|interview|invite|invitation|contact|hear)\b|\b(?:we|you)\s+(?:will|may|might|could)\s+(?:receive|be sent|get)\s+(?:an?\s+)?(?:email|invitation|link|request)\b[\s\S]{0,120}\b(?:schedule|scheduling|interview|meet|call)\b`)
	passiveReviewRE    = ci(`\b(?:(?:we|our team|the team)\s+(?:will|may|might|are|is|'ll|'re|are currently)?\s*(?:review|reviewing|evaluate|evaluating|consider|read|reach out)|currently reviewing resumes?|will notify you|(?:are|will be)?\s*keeping your application on file)\b`)
	passiveStatusRE    = ci(`\b(?:check|view|track|follow)\s+(?:the\s+)?(?:application\s+)?status\b|\b(?:application|resume|profile)\s+(?:is\s+)?(?:under review|being reviewed|on file)\b`)
	activeStatusRE     = ci(`\b(?:please|must|need to|action required|requires your attention|reply|respond|schedule|upload|provide|submit|select|choose|confirm|complete)\b`)

	claimTrustNameRE  = claimNameRE
	interviewedWithUs = ci(`\binterview(?:ed|ing)?\s+with\s+(?:us|our team|the team)\b`)

	jobDigestRE                = ci(`\b(?:newsletter|weekly digest|recommended jobs|picked for you|latest jobs|view all jobs|similar jobs)\b`)
	listFooterRE               = ci(`\b(?:unsubscribe|manage (?:your )?(?:email |notification |subscription )?preferences|email preferences)\b`)
	candidateInterviewInviteRE = ci(`\b(?:your interview|interview invitation|invite you to (?:an? )?interview)\b`)
	candidateApplicationRE     = ci(`\b(?:your (?:job )?(?:application|candidacy|candidate profile)|(?:time|effort) to (?:apply|submit)|thank(?:s| you)(?: so much)?(?: again)? for (?:applying|your (?:interest|application))|you (?:applied|were not selected))\b`)
	conditionalClauseRE        = ci(`\b(?:if|whether|either|unless|should you)\b`)
	softDecisionRE             = ci(`\bunfortunately\b[^.!?]{0,200}?\b(?:decided|decision|(?:other|another|different) candidates?|no longer (?:available|open|accepting|considering)|not (?:be )?(?:proceed|progress|mov|select|advanc|pursu)\w*)\b`)
	softVerdictRE              = ci(`\bunfortunately\b[^.!?]{0,200}?\b(?:decided|decision|no longer (?:available|open|accepting|considering)|not (?:be )?(?:proceed|progress|mov|select|advanc|pursu)\w*)\b`)
	outboundConfirmRE          = ci(`\b(?:i (?:confirm|am available|can meet)|thanks?[, ]+(?:i )?(?:confirm|am available)|(?:my|i'?m) availability|please (?:let me know|reply) with (?:a )?(?:time|slot)|works for me)\b`)

	recruitingScreenSubj = ci(`^\s*action needed:\s*next steps for .+ role\s*$`)
	linkedInRejectedTpl  = ci(`\bemail_jobs_application_rejected_01\b`)
	linkedInUpdateFrom   = lazyregexp.New(`(?i)\bYour update from\s+[A-Z][A-Za-z0-9.&'-]+`)
	linkedInWillNot      = ci(`\bUnfortunately,\s*we will not be moving forward with your application\b`)
	linkedInAppliedOn    = ci(`\bApplied on\b`)
	jobsNoReplyRE        = ci(`(?:^|[<\s])jobs-noreply@linkedin\.com\b`)
	jobAlertSubjectRE    = ci(`\bjobs? alert\b`)

	meetingPlatformSrc = `meet\.google\.com|google meet link|teams\.microsoft\.com|[a-z0-9.-]*zoom\.us|calendar\.google\.com/calendar/(?:event|appointments/schedules)|smartrecruiters\.com/app/interviews/schedule`
	datetimeSrc        = `\b\d{1,2}:\d{2}\s?(?:am|pm)\b|\b(?:today|tomorrow|(?:mon|tue|wed|thu|fri|sat|sun)[a-z]*)\s+(?:at\s+)?\d{1,2}\s?(?:am|pm)\b|\b(?:mon|tue|wed|thu|fri|sat|sun)[a-z]*,?\s+(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+\d{1,2}\b|\b(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\s+\d{1,2}(?:st|nd|rd|th)?\s+at\s+\d`
)
