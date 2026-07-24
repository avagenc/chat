// Package instructionbase holds the base instruction template shared by every
// agent kind. It has no dependencies of its own so both internal/agent/ava and
// internal/agent/specialist can import it to compose their own kind text into
// the base without importing each other (no import cycle). The %s marker is the
// slot each kind fills with its own kind text; {channel_instruction},
// {run_instruction}, and {sess_instruction} stay as literal ADK state-delta
// placeholders resolved per run.
package instructionbase

import _ "embed"

//go:embed instruction.txt
var Template string
