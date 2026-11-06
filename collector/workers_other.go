//go:build !linux

package collector

func processClockTicks() float64 { return 0 }
func readWorkerProcesses(string, string) WorkerView {
	return WorkerView{Error: "OS worker sampling requires Linux", Processes: []WorkerProcess{}}
}
