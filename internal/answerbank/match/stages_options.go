package match

// optionStages fills the option-picker and self-identification slots
// (parity sections PO and SI): PickOption, AutoAcknowledge, SelfIDCheck and
// OutputGuards.
func optionStages(st *Stages) {
	st.PickOption = pickOptionStage
	st.AutoAcknowledge = acknowledgeStage
	st.SelfIDCheck = selfIDCheck
	st.OutputGuards = outputGuards
}
