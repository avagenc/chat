package agent

import (
	_ "embed"
)

const SessionInstructionDeltaKey string = "temp:sess_instruction"
const RunInstructionDeltaKey string = "temp:run_instruction"

//go:embed base-instruction.txt
var baseInstruction string

func Instruction() string {
	return baseInstruction +
		"\n{" + RunInstructionDeltaKey + "?}\n{" + SessionInstructionDeltaKey + "?}"
}
