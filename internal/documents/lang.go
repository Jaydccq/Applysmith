package documents

import (
	"fmt"
	"sort"
	"time"

	"applysmith/internal/documents/assets"
)

// language holds the words the templates and the deterministic builders print.
// Every supported language is written in Latin script, which the shipped
// fonts cover; adding another script means adding fonts, so it is a template
// change, not a setting.
type language struct {
	labels     assets.ResumeLabels
	skills     string
	present    string
	salutation string
	closing    string
	relocation string
	// leadIn opens the company paragraph and takes the company's name;
	// thisTeam stands for it when the name is unknown.
	leadIn   string
	thisTeam string
	months   [12]string
	abbrev   bool // a résumé date prints a month as its first three letters
}

var languages = map[string]language{
	"en": {
		labels:     assets.EnglishLabels,
		skills:     "Skills",
		present:    "Present",
		salutation: "Dear Hiring Team,",
		closing:    "Kind regards,",
		relocation: "open to relocation",
		leadIn:     "What draws me to %s:",
		thisTeam:   "this team",
		months:     [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
		abbrev:     true,
	},
	"de": {
		labels:     assets.ResumeLabels{Summary: "Profil", Competencies: "Kernkompetenzen", Experience: "Berufserfahrung", Projects: "Ausgewählte Projekte", Education: "Ausbildung", Languages: "Sprachkenntnisse"},
		skills:     "Kenntnisse",
		present:    "heute",
		salutation: "Sehr geehrtes Team,",
		closing:    "Freundliche Grüße,",
		relocation: "umzugsbereit",
		leadIn:     "Was mich an %s anspricht:",
		thisTeam:   "diesem Team",
		months:     [12]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
	},
	"fr": {
		labels:     assets.ResumeLabels{Summary: "Profil", Competencies: "Compétences clés", Experience: "Expérience professionnelle", Projects: "Projets sélectionnés", Education: "Formation", Languages: "Langues"},
		skills:     "Compétences",
		present:    "aujourd'hui",
		salutation: "Madame, Monsieur,",
		closing:    "Cordialement,",
		relocation: "ouvert à la mobilité",
		leadIn:     "Ce qui m'attire chez %s :",
		thisTeam:   "cette équipe",
		months:     [12]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"},
	},
	"es": {
		labels:     assets.ResumeLabels{Summary: "Perfil", Competencies: "Competencias clave", Experience: "Experiencia profesional", Projects: "Proyectos destacados", Education: "Formación", Languages: "Idiomas"},
		skills:     "Habilidades",
		present:    "actualidad",
		salutation: "Estimado equipo de selección,",
		closing:    "Atentamente,",
		relocation: "disponible para reubicación",
		leadIn:     "Lo que me atrae de %s:",
		thisTeam:   "este equipo",
		months:     [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
	},
	"it": {
		labels:     assets.ResumeLabels{Summary: "Profilo", Competencies: "Competenze chiave", Experience: "Esperienza professionale", Projects: "Progetti selezionati", Education: "Formazione", Languages: "Lingue"},
		skills:     "Competenze",
		present:    "oggi",
		salutation: "Gentile team di selezione,",
		closing:    "Cordiali saluti,",
		relocation: "disponibile al trasferimento",
		leadIn:     "Ciò che mi attrae di %s:",
		thisTeam:   "questo team",
		months:     [12]string{"gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"},
	},
	"pt": {
		labels:     assets.ResumeLabels{Summary: "Perfil", Competencies: "Competências principais", Experience: "Experiência profissional", Projects: "Projetos selecionados", Education: "Formação", Languages: "Idiomas"},
		skills:     "Habilidades",
		present:    "atual",
		salutation: "Prezada equipe de recrutamento,",
		closing:    "Atenciosamente,",
		relocation: "disponível para mudança",
		leadIn:     "O que me atrai em %s:",
		thisTeam:   "esta equipe",
		months:     [12]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"},
	},
	"nl": {
		labels:     assets.ResumeLabels{Summary: "Profiel", Competencies: "Kerncompetenties", Experience: "Werkervaring", Projects: "Geselecteerde projecten", Education: "Opleiding", Languages: "Talen"},
		skills:     "Vaardigheden",
		present:    "heden",
		salutation: "Geachte selectiecommissie,",
		closing:    "Met vriendelijke groet,",
		relocation: "bereid te verhuizen",
		leadIn:     "Wat mij aanspreekt bij %s:",
		thisTeam:   "dit team",
		months:     [12]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"},
	},
}

// Languages lists the supported language tags.
func Languages() []string {
	out := make([]string, 0, len(languages))
	for k := range languages {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lookupLanguage(tag string) (language, bool) {
	l, ok := languages[tag]
	return l, ok
}

// date writes a letter date in the language: the English form is
// month first, day, comma, year; the others day, month, year.
func (l language) date(tag string, t time.Time) string {
	month := l.months[t.Month()-1]
	if tag == "en" {
		return fmt.Sprintf("%s %d, %d", month, t.Day(), t.Year())
	}
	return fmt.Sprintf("%d %s %d", t.Day(), month, t.Year())
}
