// Package agent holds what every agent in the roster shares and neither Ava
// nor the specialists own alone: the base instruction layer with its state
// delta keys (this file), and the billing of a run's token usage against the
// user's wallet (biller.go, usage.go). Both are imported by ava and
// specialist; this package imports neither, so the roster stays a forest.
package agent

import _ "embed"

const ChannelInstructionDeltaKey string = "channel_instruction"
const RunInstructionDeltaKey string = "run_instruction"
const SessionInstructionDeltaKey string = "sess_instruction"

//go:embed instruction.txt
var Instruction string
