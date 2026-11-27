//go:build !unix

package process

// Liveness is not checked on other platforms. Reporting every process as
// gone keeps callers on their safe path, such as not touching a service.
func alive(int) bool { return false }
