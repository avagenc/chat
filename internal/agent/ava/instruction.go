package ava

import (
	_ "embed"
	"fmt"

	"github.com/avagenc/chat/internal/agent"
)

//go:embed instruction.txt
var instruction string

func Instruction() string {
	return fmt.Sprintf(agent.Instruction, instruction)
}
