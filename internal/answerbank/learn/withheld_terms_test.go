package learn

import "testing"

// The learn-side classifier withholds the self-identification and secret
// categories in every language of the term tables. Questions in the
// non-Latin scripts are written as escapes: the repository keeps its source in
// ASCII and Latin letters.

// termCategories are the categories the per-language questions below name, in
// the order of the questions.
var termCategories = []string{"self-id:gender", "self-id:orientation", "self-id:ethnicity", "self-id:disability", "self-id:veteran", "self-id:pronouns", "self-id:date-of-birth", "secret:password", "secret:one-time-code", "secret:payment", "secret:government-id", "secret:payment"}

var termQuestions = map[string][]string{
	"en": {"Gender identity", "Sexual orientation", "Race/Ethnicity", "Disability status", "Veteran status", "Preferred pronouns", "Date of birth", "Password", "One-time code", "Credit card number", "SSN", "Bank account"},
	"de": {"Geschlecht", "Sexuelle Orientierung", "Ethnie", "Behinderung", "Veteranenstatus", "Pronomen", "Geburtsdatum", "Passwort", "Einmalcode", "Kreditkartennummer", "Sozialversicherungsnummer", "Kontonummer"},
	"fr": {"Genre", "Orientation sexuelle", "Origine ethnique", "Situation de handicap", "Ancien combattant", "Pronoms", "Date de naissance", "Mot de passe", "Code de vérification", "Numéro de carte", "Numéro de sécurité sociale", "Compte bancaire"},
	"es": {"Sexo", "Orientación sexual", "Etnia", "Discapacidad", "Veterano", "Pronombres", "Fecha de nacimiento", "Contraseña", "Código de verificación", "Tarjeta de crédito", "Número de pasaporte", "Cuenta bancaria"},
	"pt": {"Gênero", "Orientação sexual", "Etnia", "Deficiência", "Veterano", "Pronomes", "Data de nascimento", "Senha", "Código de verificação", "Cartão de crédito", "CPF", "Conta bancária"},
	"it": {"Sesso", "Orientamento sessuale", "Etnia", "Disabilità", "Veterano", "Pronomi", "Data di nascita", "Password", "Codice di verifica", "Carta di credito", "Codice fiscale", "Conto corrente"},
	"nl": {"Geslacht", "Seksuele geaardheid", "Etniciteit", "Beperking", "Veteraan", "Voornaamwoorden", "Geboortedatum", "Wachtwoord", "Verificatiecode", "Creditcard", "Burgerservicenummer", "Bankrekening"},
	"zh": {"\u6027\u522b", "\u6027\u53d6\u5411", "\u79cd\u65cf", "\u6b8b\u75be", "\u9000\u4f0d\u519b\u4eba", "\u4ee3\u8bcd", "\u51fa\u751f\u65e5\u671f", "\u5bc6\u7801", "\u9a8c\u8bc1\u7801", "\u4fe1\u7528\u5361", "\u8eab\u4efd\u8bc1", "\u94f6\u884c\u8d26\u6237"},
	"ja": {"\u6027\u5225", "\u6027\u7684\u6307\u5411", "\u4eba\u7a2e", "\u969c\u5bb3", "\u9000\u5f79\u8ecd\u4eba", "\u4ee3\u540d\u8a5e", "\u751f\u5e74\u6708\u65e5", "\u30d1\u30b9\u30ef\u30fc\u30c9", "\u8a8d\u8a3c\u30b3\u30fc\u30c9", "\u30af\u30ec\u30b8\u30c3\u30c8\u30ab\u30fc\u30c9", "\u30de\u30a4\u30ca\u30f3\u30d0\u30fc", "\u53e3\u5ea7\u756a\u53f7"},
	"ko": {"\uc131\ubcc4", "\uc131\uc801 \uc9c0\ud5a5", "\uc778\uc885", "\uc7a5\uc560", "\uc7ac\ud5a5\uad70\uc778", "\ub300\uba85\uc0ac", "\uc0dd\ub144\uc6d4\uc77c", "\ube44\ubc00\ubc88\ud638", "\uc778\uc99d\ubc88\ud638", "\uc2e0\uc6a9\uce74\ub4dc", "\uc8fc\ubbfc\ub4f1\ub85d\ubc88\ud638", "\uacc4\uc88c\ubc88\ud638"},
}

var ordinaryQuestions = map[string][]string{
	"en": {"City", "Current employer", "Why do you want to work here?", "Postal code"},
	"de": {"Stadt", "Aktueller Arbeitgeber", "Postleitzahl", "Warum möchten Sie bei uns arbeiten?"},
	"fr": {"Ville", "Employeur actuel", "Code postal", "Pourquoi voulez-vous travailler ici ?"},
	"es": {"Ciudad", "Empleador actual", "Código postal", "¿Por qué quiere trabajar aquí?"},
	"pt": {"Cidade", "Empregador atual", "Código postal", "Por que quer trabalhar aqui?"},
	"it": {"Città", "Datore di lavoro attuale", "Codice postale", "Perché vuoi lavorare qui?"},
	"nl": {"Stad", "Huidige werkgever", "Postcode", "Waarom wil je hier werken?"},
	"zh": {"\u57ce\u5e02", "\u5f53\u524d\u96c7\u4e3b", "\u90ae\u653f\u7f16\u7801", "\u4f60\u4e3a\u4ec0\u4e48\u60f3\u5728\u8fd9\u91cc\u5de5\u4f5c"},
	"ja": {"\u5e02\u533a\u753a\u6751", "\u73fe\u5728\u306e\u52e4\u52d9\u5148", "\u90f5\u4fbf\u756a\u53f7", "\u306a\u305c\u3053\u3053\u3067\u50cd\u304d\u305f\u3044\u3067\u3059\u304b"},
	"ko": {"\ub3c4\uc2dc", "\ud604\uc7ac \uc9c1\uc7a5", "\uc6b0\ud3b8\ubc88\ud638", "\uc65c \uc5ec\uae30\uc11c \uc77c\ud558\uace0 \uc2f6\uc2b5\ub2c8\uae4c"},
}

var selfIDOptions = map[string][]string{
	"en": {"Male", "Female", "Prefer not to say"},
	"de": {"Männlich", "Weiblich", "Divers", "Keine Angabe"},
	"fr": {"Homme", "Femme", "Non binaire", "Je préfère ne pas répondre"},
	"es": {"Hombre", "Mujer", "No binario", "Prefiero no decirlo"},
	"pt": {"Homem", "Mulher", "Não binário", "Prefiro não dizer"},
	"it": {"Uomo", "Donna", "Non binario", "Preferisco non rispondere"},
	"nl": {"Man", "Vrouw", "Non-binair", "Zeg ik liever niet"},
	"zh": {"\u7537", "\u5973", "\u4e0d\u613f\u900f\u9732"},
	"ja": {"\u7537\u6027", "\u5973\u6027", "\u56de\u7b54\u3057\u306a\u3044"},
	"ko": {"\ub0a8\uc131", "\uc5ec\uc131", "\uc751\ub2f5\ud558\uc9c0 \uc54a\uc74c"},
}

func TestWithheld_Terms_PerLanguage(t *testing.T) {
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			for i, q := range termQuestions[lang] {
				w, label := classify(q, "text", "", "", nil)
				if label != termCategories[i] || w == WithheldNone {
					t.Errorf("%q: got %q (%s), want %q", q, label, w, termCategories[i])
				}
			}
		})
	}
}

func TestWithheld_Terms_OrdinaryQuestionsStayLearnable(t *testing.T) {
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			for _, q := range ordinaryQuestions[lang] {
				if w, label := classify(q, "text", "", "", nil); w != WithheldNone {
					t.Errorf("%q is withheld as %q", q, label)
				}
			}
		})
	}
}

// A list of options that reads as a self-identification set is withheld
// whatever the question says and whatever control kind the sender names.
func TestWithheld_Terms_OptionSetsPerLanguage(t *testing.T) {
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			for _, kind := range []string{"select", "radio-group"} {
				if w, label := classify("Choose one", kind, "", "", selfIDOptions[lang]); w != WithheldSelfID || label != "self-id:other" {
					t.Errorf("%s: got %q (%s)", kind, label, w)
				}
			}
			if w, _ := classify("Choose one", "text", "", "", selfIDOptions[lang]); w != WithheldSelfID {
				t.Error("the control kind is the client's word, so a text kind still has its option set read")
			}
		})
	}
	for _, opts := range [][]string{{"Yes", "No"}, {"Yes", "No", "Prefer not to say"}, {"Remote", "Hybrid", "On-site"}, {"Manager", "Engineer"}} {
		if w, _ := classify("Choose one", "select", "", "", opts); w != WithheldNone {
			t.Errorf("%v is not a self-identification set", opts)
		}
	}
}

func TestWithheld_Terms_FoldingDefeatsInvisibleCharacters(t *testing.T) {
	for _, q := range []string{"Pass\u200bword", "Password\u200bx", "\uff30\uff41\uff53\uff53\uff57\uff4f\uff52\uff44", "PASSW\u00d6RD", "Pa\u00ad\u00adssword"} {
		if w, _ := classify(q, "text", "", "", nil); w != WithheldPassword {
			t.Errorf("%q: %q", q, w)
		}
	}
}

// Polish, Swedish, Russian, Arabic, Hindi, Danish, Norwegian, Finnish and
// Turkish join the term tables. Their test data is added to the shared tables
// so the per-language tests cover them like the first ten. Text outside Latin
// letters is escaped, as in the tables.
func init() {
	termQuestions["pl"] = []string{"Płeć", "Orientacja seksualna", "Pochodzenie etniczne", "Niepełnosprawność", "Weteran", "Zaimki", "Data urodzenia", "Hasło", "Kod jednorazowy", "Numer karty", "Numer paszportu", "Numer konta"}
	termQuestions["sv"] = []string{"Kön", "Sexuell läggning", "Etnicitet", "Funktionsnedsättning", "Veteran", "Pronomen", "Födelsedatum", "Lösenord", "Engångskod", "Kortnummer", "Personnummer", "Bankkonto"}
	termQuestions["ru"] = []string{"\u041f\u043e\u043b", "\u0421\u0435\u043a\u0441\u0443\u0430\u043b\u044c\u043d\u0430\u044f \u043e\u0440\u0438\u0435\u043d\u0442\u0430\u0446\u0438\u044f", "\u041d\u0430\u0446\u0438\u043e\u043d\u0430\u043b\u044c\u043d\u043e\u0441\u0442\u044c", "\u0418\u043d\u0432\u0430\u043b\u0438\u0434\u043d\u043e\u0441\u0442\u044c", "\u0412\u0435\u0442\u0435\u0440\u0430\u043d", "\u041c\u0435\u0441\u0442\u043e\u0438\u043c\u0435\u043d\u0438\u044f", "\u0414\u0430\u0442\u0430 \u0440\u043e\u0436\u0434\u0435\u043d\u0438\u044f", "\u041f\u0430\u0440\u043e\u043b\u044c", "\u041a\u043e\u0434 \u043f\u043e\u0434\u0442\u0432\u0435\u0440\u0436\u0434\u0435\u043d\u0438\u044f", "\u041d\u043e\u043c\u0435\u0440 \u043a\u0430\u0440\u0442\u044b", "\u041f\u0430\u0441\u043f\u043e\u0440\u0442", "\u041d\u043e\u043c\u0435\u0440 \u0441\u0447\u0435\u0442\u0430"}
	termQuestions["ar"] = []string{"\u0627\u0644\u062c\u0646\u0633", "\u0627\u0644\u0645\u064a\u0648\u0644 \u0627\u0644\u062c\u0646\u0633\u064a\u0629", "\u0627\u0644\u0639\u0631\u0642", "\u0627\u0644\u0625\u0639\u0627\u0642\u0629", "\u0645\u062d\u0627\u0631\u0628 \u0642\u062f\u064a\u0645", "\u0627\u0644\u0636\u0645\u0627\u0626\u0631", "\u062a\u0627\u0631\u064a\u062e \u0627\u0644\u0645\u064a\u0644\u0627\u062f", "\u0643\u0644\u0645\u0629 \u0627\u0644\u0645\u0631\u0648\u0631", "\u0631\u0645\u0632 \u0627\u0644\u062a\u062d\u0642\u0642", "\u0631\u0642\u0645 \u0627\u0644\u0628\u0637\u0627\u0642\u0629", "\u062c\u0648\u0627\u0632 \u0627\u0644\u0633\u0641\u0631", "\u0631\u0642\u0645 \u0627\u0644\u062d\u0633\u0627\u0628"}
	termQuestions["hi"] = []string{"\u0932\u093f\u0902\u0917", "\u092f\u094c\u0928 \u0930\u0941\u091d\u093e\u0928", "\u091c\u093e\u0924\u0940\u092f\u0924\u093e", "\u0935\u093f\u0915\u0932\u093e\u0902\u0917\u0924\u093e", "\u092a\u0942\u0930\u094d\u0935 \u0938\u0948\u0928\u093f\u0915", "\u0938\u0930\u094d\u0935\u0928\u093e\u092e", "\u091c\u0928\u094d\u092e \u0924\u093f\u0925\u093f", "\u092a\u093e\u0938\u0935\u0930\u094d\u0921", "\u0938\u0924\u094d\u092f\u093e\u092a\u0928 \u0915\u094b\u0921", "\u0915\u093e\u0930\u094d\u0921 \u0928\u0902\u092c\u0930", "\u092a\u093e\u0938\u092a\u094b\u0930\u094d\u091f \u0928\u0902\u092c\u0930", "\u092c\u0948\u0902\u0915 \u0916\u093e\u0924\u093e"}
	termQuestions["da"] = []string{"Køn", "Seksuel orientering", "Etnicitet", "Handicap", "Veteran", "Pronominer", "Fødselsdato", "Adgangskode", "Engangskode", "Kortnummer", "CPR-nummer", "Bankkonto"}
	termQuestions["no"] = []string{"Kjønn", "Seksuell orientering", "Etnisitet", "Funksjonsnedsettelse", "Veteran", "Pronomen", "Fødselsdato", "Passord", "Engangskode", "Kortnummer", "Fødselsnummer", "Bankkonto"}
	termQuestions["fi"] = []string{"Sukupuoli", "Seksuaalinen suuntautuminen", "Etninen tausta", "Vammaisuus", "Veteraani", "Pronominit", "Syntymäaika", "Salasana", "Vahvistuskoodi", "Korttinumero", "Henkilötunnus", "Pankkitili"}
	termQuestions["tr"] = []string{"Cinsiyet", "Cinsel yönelim", "Etnik köken", "Engellilik durumu", "Gazi", "Zamirler", "Doğum tarihi", "Şifre", "Doğrulama kodu", "Kart numarası", "TC kimlik numarası", "Banka hesabı"}
	ordinaryQuestions["pl"] = []string{"Miasto", "Obecny pracodawca", "Kod pocztowy", "Dlaczego chcesz u nas pracować?"}
	ordinaryQuestions["sv"] = []string{"Stad", "Nuvarande arbetsgivare", "Postnummer", "Varför vill du arbeta här?"}
	ordinaryQuestions["ru"] = []string{"\u0413\u043e\u0440\u043e\u0434", "\u0422\u0435\u043a\u0443\u0449\u0438\u0439 \u0440\u0430\u0431\u043e\u0442\u043e\u0434\u0430\u0442\u0435\u043b\u044c", "\u041f\u043e\u0447\u0442\u043e\u0432\u044b\u0439 \u0438\u043d\u0434\u0435\u043a\u0441", "\u041f\u043e\u0447\u0435\u043c\u0443 \u0432\u044b \u0445\u043e\u0442\u0438\u0442\u0435 \u0437\u0434\u0435\u0441\u044c \u0440\u0430\u0431\u043e\u0442\u0430\u0442\u044c?"}
	ordinaryQuestions["ar"] = []string{"\u0627\u0644\u0645\u062f\u064a\u0646\u0629", "\u0635\u0627\u062d\u0628 \u0627\u0644\u0639\u0645\u0644 \u0627\u0644\u062d\u0627\u0644\u064a", "\u0627\u0644\u0631\u0645\u0632 \u0627\u0644\u0628\u0631\u064a\u062f\u064a", "\u0644\u0645\u0627\u0630\u0627 \u062a\u0631\u064a\u062f \u0627\u0644\u0639\u0645\u0644 \u0647\u0646\u0627\u061f"}
	ordinaryQuestions["hi"] = []string{"\u0936\u0939\u0930", "\u0935\u0930\u094d\u0924\u092e\u093e\u0928 \u0928\u093f\u092f\u094b\u0915\u094d\u0924\u093e", "\u0921\u093e\u0915 \u0915\u094b\u0921", "\u0906\u092a \u092f\u0939\u093e\u0901 \u0915\u093e\u092e \u0915\u094d\u092f\u094b\u0902 \u0915\u0930\u0928\u093e \u091a\u093e\u0939\u0924\u0947 \u0939\u0948\u0902?"}
	ordinaryQuestions["da"] = []string{"By", "Nuværende arbejdsgiver", "Postnummer", "Hvorfor vil du arbejde her?"}
	ordinaryQuestions["no"] = []string{"By", "Nåværende arbeidsgiver", "Postnummer", "Hvorfor vil du jobbe her?"}
	ordinaryQuestions["fi"] = []string{"Kaupunki", "Nykyinen työnantaja", "Postinumero", "Miksi haluat työskennellä täällä?"}
	ordinaryQuestions["tr"] = []string{"Şehir", "Mevcut işveren", "Posta kodu", "Neden burada çalışmak istiyorsun?"}
	selfIDOptions["pl"] = []string{"Mężczyzna", "Kobieta", "Wolę nie podawać"}
	selfIDOptions["sv"] = []string{"Man", "Kvinna", "Vill inte uppge"}
	selfIDOptions["ru"] = []string{"\u041c\u0443\u0436\u0441\u043a\u043e\u0439", "\u0416\u0435\u043d\u0441\u043a\u0438\u0439", "\u041d\u0435 \u0445\u043e\u0447\u0443 \u0443\u043a\u0430\u0437\u044b\u0432\u0430\u0442\u044c"}
	selfIDOptions["ar"] = []string{"\u0630\u0643\u0631", "\u0623\u0646\u062b\u0649", "\u0623\u0641\u0636\u0644 \u0639\u062f\u0645 \u0627\u0644\u0625\u062c\u0627\u0628\u0629"}
	selfIDOptions["hi"] = []string{"\u092a\u0941\u0930\u0941\u0937", "\u092e\u0939\u093f\u0932\u093e", "\u092c\u0924\u093e\u0928\u093e \u0928\u0939\u0940\u0902 \u091a\u093e\u0939\u0924\u093e"}
	selfIDOptions["da"] = []string{"Mand", "Kvinde", "Ikke binær"}
	selfIDOptions["no"] = []string{"Mann", "Kvinne", "Ikke binær"}
	selfIDOptions["fi"] = []string{"Mies", "Nainen", "En halua kertoa"}
	selfIDOptions["tr"] = []string{"Erkek", "Kadın", "Belirtmek istemiyorum"}
}
