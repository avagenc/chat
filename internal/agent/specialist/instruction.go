package specialist

import (
	_ "embed"
	"fmt"

	"github.com/avagenc/chat/internal/agent/instructionbase"
)

//go:embed instruction.txt
var kindInstruction string

func Instruction() string {
	return fmt.Sprintf(instructionbase.Template, kindInstruction)
}
