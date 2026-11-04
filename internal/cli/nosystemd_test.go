package cli

// Tests never touch the real agent service of the machine they run on.
func init() { restartAgentUnit = func() bool { return false } }
