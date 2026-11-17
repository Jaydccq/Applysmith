package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// countryNames maps an ISO 3166-1 alpha-2 code to the English short name a form
// writes. The profile stores the code; a composite answer written in prose uses
// the name.
const countryNames = `
AD:Andorra AE:United Arab Emirates AF:Afghanistan AG:Antigua and Barbuda AI:Anguilla AL:Albania AM:Armenia
AO:Angola AQ:Antarctica AR:Argentina AS:American Samoa AT:Austria AU:Australia AW:Aruba AX:Aland Islands
AZ:Azerbaijan BA:Bosnia and Herzegovina BB:Barbados BD:Bangladesh BE:Belgium BF:Burkina Faso BG:Bulgaria
BH:Bahrain BI:Burundi BJ:Benin BL:Saint Barthelemy BM:Bermuda BN:Brunei BO:Bolivia BQ:Bonaire, Sint Eustatius and Saba
BR:Brazil BS:Bahamas BT:Bhutan BV:Bouvet Island BW:Botswana BY:Belarus BZ:Belize CA:Canada CC:Cocos Islands
CD:Democratic Republic of the Congo CF:Central African Republic CG:Congo CH:Switzerland CI:Cote d'Ivoire
CK:Cook Islands CL:Chile CM:Cameroon CN:China CO:Colombia CR:Costa Rica CU:Cuba CV:Cape Verde CW:Curacao
CX:Christmas Island CY:Cyprus CZ:Czechia DE:Germany DJ:Djibouti DK:Denmark DM:Dominica DO:Dominican Republic
DZ:Algeria EC:Ecuador EE:Estonia EG:Egypt EH:Western Sahara ER:Eritrea ES:Spain ET:Ethiopia FI:Finland
FJ:Fiji FK:Falkland Islands FM:Micronesia FO:Faroe Islands FR:France GA:Gabon GB:United Kingdom GD:Grenada
GE:Georgia GF:French Guiana GG:Guernsey GH:Ghana GI:Gibraltar GL:Greenland GM:Gambia GN:Guinea GP:Guadeloupe
GQ:Equatorial Guinea GR:Greece GS:South Georgia and the South Sandwich Islands GT:Guatemala GU:Guam
GW:Guinea-Bissau GY:Guyana HK:Hong Kong HM:Heard Island and McDonald Islands HN:Honduras HR:Croatia HT:Haiti
HU:Hungary ID:Indonesia IE:Ireland IL:Israel IM:Isle of Man IN:India IO:British Indian Ocean Territory IQ:Iraq
IR:Iran IS:Iceland IT:Italy JE:Jersey JM:Jamaica JO:Jordan JP:Japan KE:Kenya KG:Kyrgyzstan KH:Cambodia
KI:Kiribati KM:Comoros KN:Saint Kitts and Nevis KP:North Korea KR:South Korea KW:Kuwait KY:Cayman Islands
KZ:Kazakhstan LA:Laos LB:Lebanon LC:Saint Lucia LI:Liechtenstein LK:Sri Lanka LR:Liberia LS:Lesotho
LT:Lithuania LU:Luxembourg LV:Latvia LY:Libya MA:Morocco MC:Monaco MD:Moldova ME:Montenegro MF:Saint Martin
MG:Madagascar MH:Marshall Islands MK:North Macedonia ML:Mali MM:Myanmar MN:Mongolia MO:Macao
MP:Northern Mariana Islands MQ:Martinique MR:Mauritania MS:Montserrat MT:Malta MU:Mauritius MV:Maldives
MW:Malawi MX:Mexico MY:Malaysia MZ:Mozambique NA:Namibia NC:New Caledonia NE:Niger NF:Norfolk Island
NG:Nigeria NI:Nicaragua NL:Netherlands NO:Norway NP:Nepal NR:Nauru NU:Niue NZ:New Zealand OM:Oman PA:Panama
PE:Peru PF:French Polynesia PG:Papua New Guinea PH:Philippines PK:Pakistan PL:Poland PM:Saint Pierre and Miquelon
PN:Pitcairn PR:Puerto Rico PS:Palestine PT:Portugal PW:Palau PY:Paraguay QA:Qatar RE:Reunion RO:Romania
RS:Serbia RU:Russia RW:Rwanda SA:Saudi Arabia SB:Solomon Islands SC:Seychelles SD:Sudan SE:Sweden SG:Singapore
SH:Saint Helena SI:Slovenia SJ:Svalbard and Jan Mayen SK:Slovakia SL:Sierra Leone SM:San Marino SN:Senegal
SO:Somalia SR:Suriname SS:South Sudan ST:Sao Tome and Principe SV:El Salvador SX:Sint Maarten SY:Syria
SZ:Eswatini TC:Turks and Caicos Islands TD:Chad TF:French Southern Territories TG:Togo TH:Thailand
TJ:Tajikistan TK:Tokelau TL:Timor-Leste TM:Turkmenistan TN:Tunisia TO:Tonga TR:Turkey TT:Trinidad and Tobago
TV:Tuvalu TW:Taiwan TZ:Tanzania UA:Ukraine UG:Uganda UM:United States Minor Outlying Islands US:United States
UY:Uruguay UZ:Uzbekistan VA:Vatican City VC:Saint Vincent and the Grenadines VE:Venezuela
VG:British Virgin Islands VI:U.S. Virgin Islands VN:Vietnam VU:Vanuatu WF:Wallis and Futuna WS:Samoa
XK:Kosovo YE:Yemen YT:Mayotte ZA:South Africa ZM:Zambia ZW:Zimbabwe
`

var countryByCode = func() map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(countryNames), "\n") {
		// Entries are separated by a space after the name's last word, which
		// cannot be split on spaces alone: a code is two capitals and a colon.
		rest := strings.TrimSpace(line)
		for rest != "" {
			code := rest[:2]
			rest = rest[3:]
			end := len(rest)
			for i := 0; i+3 < len(rest); i++ {
				if rest[i] == ' ' && rest[i+1] >= 'A' && rest[i+1] <= 'Z' && rest[i+2] >= 'A' && rest[i+2] <= 'Z' && rest[i+3] == ':' {
					end = i
					break
				}
			}
			out[code] = strings.TrimSpace(rest[:end])
			rest = strings.TrimSpace(rest[min(end+1, len(rest)):])
		}
	}
	return out
}()

// countryName is the English name of an alpha-2 code, or the value itself when
// it is not a code the table knows (a name the user wrote stays as written).
func countryName(v string) string {
	v = strings.TrimSpace(v)
	if len(v) == 2 {
		if n, ok := countryByCode[strings.ToUpper(v)]; ok {
			return n
		}
	}
	return v
}

// CountryCode reads a country as the ISO 3166-1 alpha-2 code the profile
// stores: a known code in any case, or a country's English name or common
// alias ("United States", "USA", "U.K."). A region (EU) is not a country.
func CountryCode(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if up := strings.ToUpper(v); len(v) == 2 && countryByCode[up] != "" {
		return up, true
	}
	key := strings.ToLower(strings.Join(strings.Fields(v), " "))
	switch strings.ReplaceAll(key, ".", "") {
	case "us", "usa":
		return "US", true
	case "uk":
		return "GB", true
	case "uae":
		return "AE", true
	}
	code, ok := nameCode[key]
	return code, ok && countryByCode[code] != ""
}

var (
	restOfWorldRE        = lazyregexp.New(`(?i)^\s*(?:rest of (?:the )?world|(?:all )?other countries|other)\s*$`)
	residencePlaceholder = lazyregexp.New(`(?i)^\s*(?:$|select\b|choose\b|-)`)
)

// ResidenceOption is the option of a residence menu that answers for country:
// the one naming that country, else the rest-of-world option when every other
// option is another country or a placeholder; -1 when the choice is not plain.
func ResidenceOption(options []string, country string) int {
	want, ok := CountryCode(country)
	if !ok {
		return -1
	}
	rest := -1
	for i, o := range options {
		switch code, known := CountryCode(o); {
		case known && code == want:
			return i
		case known || residencePlaceholder.MatchString(o):
		case restOfWorldRE.MatchString(o) && rest < 0:
			rest = i
		default:
			return -1
		}
	}
	return rest
}

// CountryName is the English name of an alpha-2 code, or the value as written.
func CountryName(v string) string { return countryName(v) }
