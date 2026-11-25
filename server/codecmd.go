package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/PretendoNetwork/super-mario-maker/coursecode"
)

const codeUsage = `Usage:
  smm-server code 9007-0000-00BB-FD3B     the data ID the servers use for a course ID shown in the game
  smm-server code 12320059                the course ID the game shows for a data ID (the number in the server's log)
`

// runCodeCommand implements "smm-server code ...". It returns the process exit code.
func runCodeCommand(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(codeUsage)

		return 0
	}

	for _, a := range args {
		if strings.Contains(a, "-") || len(strings.TrimSpace(a)) == 16 {
			id, err := coursecode.Parse(a)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)

				return 1
			}

			fmt.Printf("%s  =  data ID %d\n", coursecode.Format(id), id)

			continue
		}

		id, err := strconv.ParseUint(strings.TrimSpace(a), 10, 32)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%q is neither a course ID (like 9007-0000-00BB-FD3B) nor a data ID (a number)\n", a)

			return 1
		}

		fmt.Printf("%s  =  data ID %d\n", coursecode.Format(uint64(id)), id)
	}

	return 0
}
