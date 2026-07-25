package agent

import _ "embed"

const ChannelInstructionDeltaKey string = "channel_instruction"
const RunInstructionDeltaKey string = "run_instruction"
const SessionInstructionDeltaKey string = "sess_instruction"

//go:embed instruction.txt
var Instruction string
