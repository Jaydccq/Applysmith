package extract

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Digits that are not a code. The extractor fails closed, so a run of digits
// that reads as a date, a phone number, an amount, a time, a ZIP code or an
// order or reference number is never a candidate, however close it sits to a
// code noun.
var (
	// phoneRE covers the grouped phone shapes; a local seven-digit number is
	// caught by the joined-digit rule.
	phoneRE = lazyregexp.New(`\+?\d{1,3}[\s.-]?\(?\d{3}\)?[\s.-]\d{3}[\s.-]\d{4}|\(?\d{3}\)[\s.-]?\d{3}[\s.-]?\d{4}`)
	// labelBeforeRE ends a text that labels the digits after it as something
	// other than a code.
	labelBeforeRE = lazyregexp.New(`(?i)(?:\b(?:order|invoice|reference|ref|ticket|tracking|case|account|acct|transaction|txn|receipt|booking|reservation|requisition|req|job|application|id|no|num|number|zip|postal|phone|tel|fax|call|ext|extension|usd|eur|gbp|cad|aud)\b\.?\s*(?:number|no\.?|num|id)?|#|[$\x{20ac}\x{a3}\x{a5}])\s*[:#.\-]?\s*$`)
	// stateZipRE ends a text with a two-letter state before a ZIP code.
	stateZipRE = lazyregexp.New(`\b[A-Z]{2}\s+$`)
	// monthBeforeRE ends a text with a month name, optionally with its day.
	monthBeforeRE = lazyregexp.New(`(?i)\b(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\b\.?\s*(?:\d{1,2}(?:st|nd|rd|th)?,?\s*)?$`)
	// labelAfterRE starts a text that gives the digits before it a unit or a
	// non-code name.
	labelAfterRE = lazyregexp.New(`(?i)^[\s:,.\-]*(?:(?:is|was)\s+)?(?:(?:your|the|a)\s+)?(?:order|invoice|reference|ref\b|ticket|tracking|case|account|transaction|receipt|booking|reservation|zip|postal|extension|ext\b|am\b|pm\b|a\.m\b|p\.m\b|hrs?\b|hours?\b|minutes?\b|mins?\b|seconds?\b|secs?\b|days?\b|usd\b|eur\b|gbp\b|cad\b|aud\b|dollars?\b|euros?\b|percent\b|%|miles?\b|km\b|kb\b|mb\b|gb\b)`)
	// qualifiedNounRE is a code noun that names something other than a
	// verification code, such as a ZIP code or a promo code.
	qualifiedNounRE = lazyregexp.New(`(?i)\b(?:zip|postal|post|area|country|dial|promo|promotional|coupon|discount|voucher|referral|gift|tracking|bar|qr|source|error|status|response|product|item|store|sort|routing|swift|currency|language|color|colour|invoice|order|ticket|reference)[\s-]+$`)
	// yearRE is a four-digit year.
	yearRE = lazyregexp.New(`^(?:19|20|21)\d{2}$`)
)

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// joinedDigits reports a digit run attached to another digit run by one
// separator, as dates, times, amounts, phone numbers and ZIP+4 are written.
func joinedDigits(text string, s span) bool {
	at := func(i int) byte {
		if i < 0 || i >= len(text) {
			return 0
		}
		return text[i]
	}
	sep := func(b byte) bool { return strings.IndexByte("/-.:,", b) >= 0 }
	return sep(at(s.end)) && isDigit(at(s.end+1)) || sep(at(s.start-1)) && isDigit(at(s.start-2))
}

func tail(text string, end, n int) string { return text[max(end-n, 0):end] }

// nonCodeDigits reports a digit-only candidate that reads as something other
// than a code. Alphanumeric candidates are never dates or phone numbers.
func nonCodeDigits(text string, c candidate) bool {
	if !isDigits(c.value) {
		return false
	}
	if joinedDigits(text, c.span) {
		return true
	}
	for _, m := range phoneRE.FindAllStringIndex(text, -1) {
		if c.span.start < m[1] && m[0] < c.span.end {
			return true
		}
	}
	before := tail(text, c.span.start, 40)
	if labelBeforeRE.MatchString(before) || monthBeforeRE.MatchString(before) {
		return true
	}
	if len(c.value) == 5 && stateZipRE.MatchString(before) {
		return true
	}
	return labelAfterRE.MatchString(text[c.span.end:min(c.span.end+24, len(text))])
}

// nonVerificationNoun reports a code noun that a qualifier turns into
// something else ("ZIP code", "promo code").
func nonVerificationNoun(text string, kw span) bool {
	return qualifiedNounRE.MatchString(tail(text, kw.start, 16))
}

// isYear reports a four-digit year. A year is rejected only in the second
// tier, where a code is identified by distance alone; directly beside a code
// noun a four-digit code that looks like a year is still a code.
func isYear(c candidate) bool { return yearRE.MatchString(c.value) }
