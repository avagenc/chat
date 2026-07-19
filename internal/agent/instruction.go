package agent

import (
	_ "embed"
)

const ChannelInstructionDeltaKey string = "channel_instruction"
const KindSpecificInstructionDeltaKey string = "kind_specific_instruction"
const RunInstructionDeltaKey string = "run_instruction"
const SessionInstructionDeltaKey string = "sess_instruction"

//go:embed instruction.txt
var baseInstruction string

func Instruction() string {
	return baseInstruction +
		"\n{" + ChannelInstructionDeltaKey + "}\n{" + KindSpecificInstructionDeltaKey + "}\n{" + RunInstructionDeltaKey + "}\n{" + SessionInstructionDeltaKey + "}"
}
