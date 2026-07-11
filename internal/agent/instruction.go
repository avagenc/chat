package agent

import (
	_ "embed"
)

const KindSpecificInstructionDeltaKey string = "kind_specific_instruction"
const SessionInstructionDeltaKey string = "sess_instruction"
const RunInstructionDeltaKey string = "run_instruction"

//go:embed instruction.txt
var baseInstruction string

func Instruction() string {
	return baseInstruction +
		"\n{" + KindSpecificInstructionDeltaKey + "}\n{" + RunInstructionDeltaKey + "}\n{" + SessionInstructionDeltaKey + "}"
}
