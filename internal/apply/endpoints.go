package apply

import "fmt"

// PortOrDefault returns port when non-zero, otherwise the caller's fallback.
// State-vs-intent precedence is selected by each caller before this helper.
func PortOrDefault(port, fallback int) int {
	if port != 0 {
		return port
	}
	return fallback
}

func HTTPURL(host string, port int) string  { return fmt.Sprintf("http://%s:%d", host, port) }
func GRPCAddr(host string, port int) string { return fmt.Sprintf("%s:%d", host, port) }
func ProbeURL(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", PortOrDefault(port, 8090), path)
}
