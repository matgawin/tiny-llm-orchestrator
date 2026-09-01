package cli

const (
	appName              = "orc"
	defaultVersion       = "dev"
	helpFlag             = "--help"
	commandCompletionUse = "completion <shell>"
	commandCompletion    = "completion"
	shellBash            = "bash"
	commandInit          = "init"
	commandProgress      = "progress"
	commandReport        = "report"
	commandRun           = "run"
	commandSandbox       = "sandbox"
	commandTimeLeft      = "time-left"
	commandVersion       = "version"
	commandUpgrade       = "upgrade"
	commandWorker        = "worker"
	stateRunning         = "running"

	initUpgradePartialTitle  = "orc init upgrade partially applied"
	initUpgradePartialResult = "result: safe independent changes were written; unresolved conflicts remain"
)
