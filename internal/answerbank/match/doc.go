// Package match is the answer-bank matcher: a pure function from a bank, a
// page host and a list of questions to one result per question. It reads no
// file, clock or random source; the current time arrives with the request.
//
// The question side lives here: sanitation, intake, classification, the route
// catalog, guards and the resolver-order pipeline. Each resolver stage after
// classification is a typed slot in Stages that answers "not handled" until a
// resolver is plugged in.
//
// The answer-bank domain model lives here as well. Its struct tags name the
// YAML keys of answer-bank.yml; the codec lives in internal/answerbank.
package match
