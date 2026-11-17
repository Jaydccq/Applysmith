package gmail

// QueryVersion identifies the embedded search vocabulary; it is stored on every
// run so a change of vocabulary is visible in the history.
const QueryVersion = "gmail-query-go-1"

// BootstrapQueries are the three searches of a first run over the trailing six
// months (the `m` unit of the search language means months, on purpose): mail
// from known ATS and job-platform senders; mail in a recruiting vocabulary
// outside the promotions and social categories; and sent mail in a follow-up
// vocabulary.
var BootstrapQueries = []string{
	"in:anywhere newer_than:6m {from:hire.lever.co from:greenhouse-mail.io from:greenhouse.io from:ashbyhq.com from:smartrecruiters.com from:icims.com from:myworkday.com from:workday.com from:lever.co from:jobvite.com from:successfactors.com from:workablemail.com from:bamboohr.com from:linkedin.com from:indeed.com}",
	`in:anywhere newer_than:6m -category:promotions -category:social {"thank you for applying" "received your application" "your application" "application submitted" "application was sent" "application status" "application update" "interview invitation" "schedule interview" "your interview" "phone screen" reschedule "available to meet" "next steps" "book a time" "please confirm" "confirm availability" "online assessment" "coding challenge" "take-home" hackerrank codesignal "offer letter" "job offer" "not moving forward" "not selected" "move forward with other candidates" "talent acquisition" "technical recruiter"}`,
	`in:sent newer_than:6m -category:promotions -category:social {"phone screen" interview assessment reschedule "follow up" "follow-up" "next steps" availability confirming confirmed "thank you for confirming"}`,
}
